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

// email_challenges.go issues, sends, verifies, and consumes email challenge
// credentials.
// Email verification and password reset each save credentials before requesting
// a notification send. A failed send leaves the saved credential available
// under its cache lifetime, and reset resends reuse any record the cache
// returns. Recent verification records enforce their own send cooldown; reset
// cooldown markers are written separately after the reset sender reports
// success.
//
// Verification compares the supplied credential with the saved value and then
// attempts deletion. Those steps use separate cache calls, and cleanup failures
// are diagnosed while a matching credential still succeeds. Failed verification
// codes increment their attempt record with its remaining lifetime rather than
// starting a new validity window. The cache determines read availability; the
// stored expiry timestamp is used when writing that failed-attempt update.

// EmailChallengeSender requests notification delivery after identity prepares
// its credentials.
type EmailChallengeSender interface {
	// SendVerifyCodeMessage sends the prepared challenge using email, site name
	// and locale text. The caller has already saved the credential; a send error
	// is returned and leaves credential cleanup with identity.
	SendVerifyCodeMessage(context.Context, string, string, string, string) error
	// SendPasswordResetMessage sends the prepared reset link to the account email.
	// Identity has already saved or reused its token; the sender returns its
	// delivery result for the caller's cooldown and failure handling.
	SendPasswordResetMessage(context.Context, string, string, string, string) error
}

// EmailChallenges prepares, verifies and cleans up email credentials around
// notification sends.
// @project-doc docs/domains/identity_and_tenancy.md#email_challenges
type EmailChallenges struct {
	cache  EmailCache           // challenge and cooldown persistence
	sender EmailChallengeSender // prepared notification message sender
}

// NewEmailChallenges retains the cache and sender used by credential
// operations. Configure both before sending messages; credential generation and
// verification use only the dependencies their particular operation needs from
// this instance.
func NewEmailChallenges(cache EmailCache, sender EmailChallengeSender) *EmailChallenges {
	return &EmailChallenges{cache: cache, sender: sender}
}

// GenerateVerifyCode returns a six-character decimal challenge using
// cryptographic random draws.
func (s *EmailChallenges) GenerateVerifyCode() (string, error) {
	// The decimal alphabet permits leading zeros in generated challenges.
	const digits = "0123456789"
	// Keep six positions as text so leading zeros remain part of the generated
	// verification credential delivered to the recipient and compared on
	// submission.
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

// SendVerifyCode saves a new verification credential and requests its email. A
// recently created record returns ErrVerifyCodeTooFrequent, while a cache read
// failure or absent record allows generation to continue. The first optional
// locale is forwarded to the sender after trimming.
//
// Saving precedes the notification request so arriving mail can be checked
// against cached state. Save failures stop delivery, but send failures leave
// the saved credential and its send-cooldown timestamp available. Creation and
// expiry use separate wall-clock reads, and the cache receives VerifyCodeTTL
// for storage.
func (s *EmailChallenges) SendVerifyCode(ctx context.Context, email, siteName string, locale ...string) error {
	// Cooldown uses a usable prior record; a failed lookup does not establish a
	// recent send, so the flow can continue to generating and saving a new
	// challenge.
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

	// Save the credential before requesting notification delivery so a recipient
	// can verify mail that arrives quickly; failed delivery keeps this saved code
	// available.
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

// VerifyCode checks the saved challenge and returns the applicable attempt or
// invalid-code error. Missing or unreadable records are invalid, and a record
// already at MaxVerifyCodeAttempts is rejected before comparison.
//
// A mismatch increments this request's record and saves it for the time
// remaining until ExpiresAt. The cache governs whether a matching record is
// available; ExpiresAt is consulted here when updating a mismatch, so
// verification assumes the cache expires saved credentials according to their
// lifetime.
//
// A match attempts deletion and succeeds even when cleanup fails. Read, compare
// and delete are separate operations, so callers cannot use this result as an
// atomic one-time credential claim across concurrent requests.
func (s *EmailChallenges) VerifyCode(ctx context.Context, email, code string) error {
	data, err := s.cache.GetVerificationCode(ctx, email)
	if err != nil || data == nil {
		// An unavailable record cannot authenticate this supplied code, so expose the
		// common invalid-code result instead of a usable verification credential.
		return ErrInvalidVerifyCode
	}

	if data.Attempts >= MaxVerifyCodeAttempts {
		return ErrVerifyCodeMaxAttempts
	}

	// Compare equal-length code bytes in constant time; a mismatch then updates
	// this record's attempt count before choosing the outward invalid or limit
	// result.
	if subtle.ConstantTimeCompare([]byte(data.Code), []byte(code)) != 1 {
		data.Attempts++
		remaining := time.Until(data.ExpiresAt)
		if remaining <= 0 {
			return ErrInvalidVerifyCode
		}
		// Retain the remaining lifetime when persisting attempts so a failed code
		// cannot start a fresh verification window. A failed write leaves persistence
		// unconfirmed.
		if err := s.cache.SetVerificationCode(ctx, email, data, remaining); err != nil {
			slog.Error("failed to update verification attempt count", "email", email, "error", err)
		}
		if data.Attempts >= MaxVerifyCodeAttempts {
			return ErrVerifyCodeMaxAttempts
		}
		return ErrInvalidVerifyCode
	}

	// Cleanup failure leaves deletion unconfirmed but does not reverse the code
	// comparison; diagnose it and let the caller continue its successfully
	// verified operation.
	if err := s.cache.DeleteVerificationCode(ctx, email); err != nil {
		slog.Error("failed to delete verification code after success", "email", email, "error", err)
	}
	return nil
}

// GeneratePasswordResetToken returns a random reset credential encoded as
// lowercase hexadecimal.
func (s *EmailChallenges) GeneratePasswordResetToken() (string, error) {
	// Thirty-two random bytes produce sixty-four hexadecimal credential
	// characters.
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// SendPasswordResetEmail prepares a reset credential and requests its link
// email. A successful cache read with a record reuses that record's token text;
// missing records and read failures select a newly generated token, which must
// be saved before the sender is called.
//
// A send failure leaves the saved credential in place, and reuse does not
// refresh its cache lifetime. Callers provide resetURL ready for the appended
// email and token query suffix because this builder does not merge existing
// query parameters. The first optional locale is trimmed and forwarded for
// notification selection.
func (s *EmailChallenges) SendPasswordResetEmail(ctx context.Context, email, siteName, resetURL string, locale ...string) error {
	var token string
	var needSaveToken bool

	existing, err := s.cache.GetPasswordResetToken(ctx, email)
	if err == nil && existing != nil {
		// Reusing the returned credential keeps resend links consistent with the
		// currently cached token and leaves its existing expiry under cache control.
		token = existing.Token
		needSaveToken = false
	} else {
		// An absent or unreadable record supplies no token to reuse, so generate and
		// save a replacement before requesting this reset notification.
		token, err = s.GeneratePasswordResetToken()
		if err != nil {
			return fmt.Errorf("generate token: %w", err)
		}
		needSaveToken = true
	}

	// Persist a generated replacement before sending its link, so sender failure
	// leaves a credential that subsequent successful cache reads can reuse for
	// resends.
	if needSaveToken {
		data := &PasswordResetTokenData{
			Token:     token,
			CreatedAt: time.Now(),
		}
		if err := s.cache.SetPasswordResetToken(ctx, email, data, PasswordResetTokenTTL); err != nil {
			return fmt.Errorf("save reset token: %w", err)
		}
	}

	// Encode email and token as independent query values so address punctuation
	// and credential text cannot become additional reset-link parameters during
	// transport.
	fullResetURL := fmt.Sprintf("%s?email=%s&token=%s", resetURL, url.QueryEscape(email), url.QueryEscape(token))

	return s.sender.SendPasswordResetMessage(ctx, email, siteName, fullResetURL, FirstEmailLocale(locale))
}

// SendPasswordResetEmailWithCooldown requests reset mail unless a recent-send
// marker is present. A cooldown skip returns nil, and a successful reset sender
// call is followed by a separate cooldown-marker write for queue workers.
//
// Marker-write errors are diagnosed while the completed sender result stays
// successful. The lookup, send and marker operations are separate, so
// overlapping calls can pass the recent-send check before either writes its
// marker. The cache's cooldown read decides how storage failures affect that
// initial check.
func (s *EmailChallenges) SendPasswordResetEmailWithCooldown(ctx context.Context, email, siteName, resetURL string, locale ...string) error {
	if s.cache.IsPasswordResetEmailInCooldown(ctx, email) {
		slog.Info("password reset email skipped due to cooldown", "email", email)
		// A recent-send marker skips notification work but keeps the ordinary
		// successful result, so callers receive no distinct cooldown error from this
		// path.
		return nil
	}

	if err := s.SendPasswordResetEmail(ctx, email, siteName, resetURL, FirstEmailLocale(locale)); err != nil {
		return err
	}

	// Mark the successful sender result afterward; marker failure leaves this
	// recent-send state unconfirmed and is diagnosed while the completed send
	// remains successful.
	if err := s.cache.SetPasswordResetEmailCooldown(ctx, email, PasswordResetEmailCooldown); err != nil {
		slog.Error("failed to set password reset cooldown", "email", email, "error", err)
	}

	return nil
}

// VerifyPasswordResetToken checks the supplied token against a readable saved
// credential. Missing records, read failures or unequal token text return
// ErrInvalidResetToken; success leaves the record available, with its lifetime
// enforced by cache storage.
func (s *EmailChallenges) VerifyPasswordResetToken(ctx context.Context, email, token string) error {
	data, err := s.cache.GetPasswordResetToken(ctx, email)
	if err != nil || data == nil {
		// A missing or unreadable reset record supplies no credential for comparison,
		// so the caller receives the same invalid-token outcome as a mismatching
		// value.
		return ErrInvalidResetToken
	}

	// Equal-length credential bytes use a constant-time comparison, while a
	// mismatching value remains invalid and leaves cache availability unchanged
	// for later checks.
	if subtle.ConstantTimeCompare([]byte(data.Token), []byte(token)) != 1 {
		return ErrInvalidResetToken
	}

	return nil
}

// ConsumePasswordResetToken verifies a reset credential and attempts its
// deletion. A comparison failure returns immediately; cleanup failure is
// diagnosed and still returns nil. Verification and deletion use separate cache
// calls, so this method does not claim atomic consumption across concurrent
// reset requests.
func (s *EmailChallenges) ConsumePasswordResetToken(ctx context.Context, email, token string) error {
	if err := s.VerifyPasswordResetToken(ctx, email, token); err != nil {
		return err
	}

	// Deletion errors leave credential cleanup unconfirmed after a successful
	// comparison; diagnose them while allowing the caller's verified reset
	// operation to proceed.
	if err := s.cache.DeletePasswordResetToken(ctx, email); err != nil {
		slog.Error("failed to delete password reset token after consumption", "email", email, "error", err)
	}
	return nil
}
