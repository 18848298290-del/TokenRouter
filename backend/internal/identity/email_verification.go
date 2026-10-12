package identity

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

const (
	VerifyCodeTTL         = 15 * time.Minute
	VerifyCodeCooldown    = 1 * time.Minute
	MaxVerifyCodeAttempts = 5

	// Password reset token settings.
	PasswordResetTokenTTL = 30 * time.Minute

	// Password reset email cooldown (prevent email bombing).
	PasswordResetEmailCooldown = 30 * time.Second
)

var (
	ErrInvalidVerifyCode     = infraerrors.BadRequest("INVALID_VERIFY_CODE", "invalid or expired verification code")
	ErrVerifyCodeTooFrequent = infraerrors.TooManyRequests("VERIFY_CODE_TOO_FREQUENT", "please wait before requesting a new code")
	ErrVerifyCodeMaxAttempts = infraerrors.TooManyRequests("VERIFY_CODE_MAX_ATTEMPTS", "too many failed attempts, please request a new code")

	// Password reset errors.
	ErrInvalidResetToken = infraerrors.BadRequest("INVALID_RESET_TOKEN", "invalid or expired password reset token")
)

// EmailCache stores email verification codes, reset tokens, and send-rate limits.
type EmailCache interface {
	GetVerificationCode(ctx context.Context, email string) (*VerificationCodeData, error)
	SetVerificationCode(ctx context.Context, email string, data *VerificationCodeData, ttl time.Duration) error
	DeleteVerificationCode(ctx context.Context, email string) error

	// Notify email verification code methods
	GetNotifyVerifyCode(ctx context.Context, email string) (*VerificationCodeData, error)
	SetNotifyVerifyCode(ctx context.Context, email string, data *VerificationCodeData, ttl time.Duration) error
	DeleteNotifyVerifyCode(ctx context.Context, email string) error

	// Password reset token methods
	GetPasswordResetToken(ctx context.Context, email string) (*PasswordResetTokenData, error)
	SetPasswordResetToken(ctx context.Context, email string, data *PasswordResetTokenData, ttl time.Duration) error
	DeletePasswordResetToken(ctx context.Context, email string) error

	// Password reset send cooldown.
	// IsPasswordResetEmailInCooldown reports whether a recent send started a cooldown.
	IsPasswordResetEmailInCooldown(ctx context.Context, email string) bool
	SetPasswordResetEmailCooldown(ctx context.Context, email string, ttl time.Duration) error

	// Notify code rate limiting per user
	IncrNotifyCodeUserRate(ctx context.Context, userID int64, window time.Duration) (int64, error)
	GetNotifyCodeUserRate(ctx context.Context, userID int64) (int64, error)
}

// VerificationCodeData tracks a code, failed attempts, and its expiry.
type VerificationCodeData struct {
	Code      string
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time // absolute expiry; used to preserve remaining TTL when updating attempts
}

// PasswordResetTokenData stores a reset token and creation time.
type PasswordResetTokenData struct {
	Token     string
	CreatedAt time.Time
}

func FirstEmailLocale(locales []string) string {
	if len(locales) == 0 {
		return ""
	}
	return strings.TrimSpace(locales[0])
}
