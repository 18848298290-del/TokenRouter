package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"time"
)

type EmailChallengeSender interface {
	SendVerifyCodeMessage(context.Context, string, string, string, string) error
	SendPasswordResetMessage(context.Context, string, string, string, string) error
}

// @project-doc docs/domains/identity_and_tenancy.md#email_challenges
type EmailChallenges struct {
	cache  EmailCache
	sender EmailChallengeSender
}

func NewEmailChallenges(cache EmailCache, sender EmailChallengeSender) *EmailChallenges {
	return &EmailChallenges{cache: cache, sender: sender}
}

// GenerateVerifyCode generates a six-digit random code.
func (s *EmailChallenges) GenerateVerifyCode() (string, error) {
	const digits = "0123456789"
	code := make([]byte, 6)
	for i := range code {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		code[i] = digits[num.Int64()]
	}
	return string(code), nil
}

// SendVerifyCode stores a registration code before sending its email.
// A send failure leaves the stored code and its cooldown in place.
func (s *EmailChallenges) SendVerifyCode(ctx context.Context, email, siteName string, locale ...string) error {
	existing, err := s.cache.GetVerificationCode(ctx, email)
	if err == nil && existing != nil {
		if time.Since(existing.CreatedAt) < VerifyCodeCooldown {
			return ErrVerifyCodeTooFrequent
		}
	}

	code, err := s.GenerateVerifyCode()
	if err != nil {
		return fmt.Errorf("generate code: %w", err)
	}

	// Store the code before sending so verification can find it as soon as mail arrives.
	data := &VerificationCodeData{
		Code:      code,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(VerifyCodeTTL),
	}
	if err := s.cache.SetVerificationCode(ctx, email, data, VerifyCodeTTL); err != nil {
		return fmt.Errorf("save verify code: %w", err)
	}

	return s.sender.SendVerifyCodeMessage(ctx, email, siteName, code, FirstEmailLocale(locale))
}

// VerifyCode checks the code and attempt limit, then tries to delete a matching code.
func (s *EmailChallenges) VerifyCode(ctx context.Context, email, code string) error {
	data, err := s.cache.GetVerificationCode(ctx, email)
	if err != nil || data == nil {
		return ErrInvalidVerifyCode
	}

	if data.Attempts >= MaxVerifyCodeAttempts {
		return ErrVerifyCodeMaxAttempts
	}

	// Compare equal-length codes in constant time.
	if subtle.ConstantTimeCompare([]byte(data.Code), []byte(code)) != 1 {
		data.Attempts++
		remaining := time.Until(data.ExpiresAt)
		if remaining <= 0 {
			return ErrInvalidVerifyCode
		}
		if err := s.cache.SetVerificationCode(ctx, email, data, remaining); err != nil {
			slog.Error("failed to update verification attempt count", "email", email, "error", err)
		}
		if data.Attempts >= MaxVerifyCodeAttempts {
			return ErrVerifyCodeMaxAttempts
		}
		return ErrInvalidVerifyCode
	}

	// Deletion failures are logged; a matching code still succeeds.
	if err := s.cache.DeleteVerificationCode(ctx, email); err != nil {
		slog.Error("failed to delete verification code after success", "email", email, "error", err)
	}
	return nil
}

// GeneratePasswordResetToken generates a secure 32-byte random token (64 hex characters).
func (s *EmailChallenges) GeneratePasswordResetToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// SendPasswordResetEmail sends a password reset email with a reset link.
func (s *EmailChallenges) SendPasswordResetEmail(ctx context.Context, email, siteName, resetURL string, locale ...string) error {
	var token string
	var needSaveToken bool

	existing, err := s.cache.GetPasswordResetToken(ctx, email)
	if err == nil && existing != nil {
		// Token exists, reuse it (allows resending email without generating new token)
		token = existing.Token
		needSaveToken = false
	} else {
		token, err = s.GeneratePasswordResetToken()
		if err != nil {
			return fmt.Errorf("generate token: %w", err)
		}
		needSaveToken = true
	}

	// Store a newly generated token before sending; retries reuse it until expiry.
	if needSaveToken {
		data := &PasswordResetTokenData{
			Token:     token,
			CreatedAt: time.Now(),
		}
		if err := s.cache.SetPasswordResetToken(ctx, email, data, PasswordResetTokenTTL); err != nil {
			return fmt.Errorf("save reset token: %w", err)
		}
	}

	// Build full reset URL with URL-encoded token and email
	fullResetURL := fmt.Sprintf("%s?email=%s&token=%s", resetURL, url.QueryEscape(email), url.QueryEscape(token))

	return s.sender.SendPasswordResetMessage(ctx, email, siteName, fullResetURL, FirstEmailLocale(locale))
}

// SendPasswordResetEmailWithCooldown skips recent sends and starts a cooldown after successful delivery.
// Queue workers use this method to limit repeated reset emails.
func (s *EmailChallenges) SendPasswordResetEmailWithCooldown(ctx context.Context, email, siteName, resetURL string, locale ...string) error {
	if s.cache.IsPasswordResetEmailInCooldown(ctx, email) {
		slog.Info("password reset email skipped due to cooldown", "email", email)
		return nil // Silent success to prevent revealing cooldown to attackers
	}

	if err := s.SendPasswordResetEmail(ctx, email, siteName, resetURL, FirstEmailLocale(locale)); err != nil {
		return err
	}

	// Set cooldown marker (Redis TTL handles expiration)
	if err := s.cache.SetPasswordResetEmailCooldown(ctx, email, PasswordResetEmailCooldown); err != nil {
		slog.Error("failed to set password reset cooldown", "email", email, "error", err)
	}

	return nil
}

// VerifyPasswordResetToken verifies the password reset token without consuming it.
func (s *EmailChallenges) VerifyPasswordResetToken(ctx context.Context, email, token string) error {
	data, err := s.cache.GetPasswordResetToken(ctx, email)
	if err != nil || data == nil {
		return ErrInvalidResetToken
	}

	// Use constant-time comparison to prevent timing attacks
	if subtle.ConstantTimeCompare([]byte(data.Token), []byte(token)) != 1 {
		return ErrInvalidResetToken
	}

	return nil
}

// ConsumePasswordResetToken verifies the token, then attempts to delete it.
// Verification and deletion are separate operations; deletion errors are logged.
func (s *EmailChallenges) ConsumePasswordResetToken(ctx context.Context, email, token string) error {
	if err := s.VerifyPasswordResetToken(ctx, email, token); err != nil {
		return err
	}

	// A deletion failure does not change the successful verification result.
	if err := s.cache.DeletePasswordResetToken(ctx, email); err != nil {
		slog.Error("failed to delete password reset token after consumption", "email", email, "error", err)
	}
	return nil
}
