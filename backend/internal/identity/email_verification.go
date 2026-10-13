package identity

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Verification and reset credentials use separate cache records, including a
// separate challenge namespace for notification addresses. The shared record
// types carry issuance and attempt data, while their callers choose send
// ordering, lifetime and whether a cleanup error changes the outward result.

const (
	// Verification lifetime, send spacing and failed-attempt limits govern both
	// login-email and notification-address challenge flows through their selected
	// cache methods.
	VerifyCodeTTL         = 15 * time.Minute
	VerifyCodeCooldown    = 1 * time.Minute
	MaxVerifyCodeAttempts = 5

	// PasswordResetTokenTTL governs the lifetime assigned to a newly saved reset
	// credential before its first notification send is requested.
	PasswordResetTokenTTL = 30 * time.Minute

	// PasswordResetEmailCooldown governs recent-send markers written after a
	// successful reset sender result, limiting subsequent queue-worker
	// notification requests.
	PasswordResetEmailCooldown = 30 * time.Second
)

// These challenge errors distinguish unavailable or mismatching credentials,
// recent verification sends and the attempt limit reached by a supplied record.
var (
	ErrInvalidVerifyCode     = infraerrors.BadRequest("INVALID_VERIFY_CODE", "invalid or expired verification code")
	ErrVerifyCodeTooFrequent = infraerrors.TooManyRequests("VERIFY_CODE_TOO_FREQUENT", "please wait before requesting a new code")
	ErrVerifyCodeMaxAttempts = infraerrors.TooManyRequests("VERIFY_CODE_MAX_ATTEMPTS", "too many failed attempts, please request a new code")

	// ErrInvalidResetToken identifies missing, unreadable or mismatching reset
	// credentials and gives password-reset callers one rejection result for those
	// cases.
	ErrInvalidResetToken = infraerrors.BadRequest("INVALID_RESET_TOKEN", "invalid or expired password reset token")
)

// EmailCache stores separate challenge credentials, reset state and
// notification send limits.
type EmailCache interface {
	// GetVerificationCode reads the saved login-email verification record for an
	// address. The Redis adapter returns missing records as errors;
	// EmailChallenges uses read failures or nil records as an invalid credential.
	// The cache owns expiry-based record availability for that address. The Redis
	// adapter decodes a fresh request record for failed-attempt changes.
	GetVerificationCode(ctx context.Context, email string) (*VerificationCodeData, error)
	// SetVerificationCode saves the supplied login-email challenge record under
	// ttl. Callers select the lifetime; failed-attempt updates pass the remaining
	// lifetime from ExpiresAt. Encoding or storage failures remain errors for the
	// flow's persistence handling.
	SetVerificationCode(ctx context.Context, email string, data *VerificationCodeData, ttl time.Duration) error
	// DeleteVerificationCode removes the address's saved login-email challenge
	// record. The Redis adapter accepts an already absent key; other cleanup
	// failures remain errors for the credential flow to diagnose or propagate.
	DeleteVerificationCode(ctx context.Context, email string) error

	// GetNotifyVerifyCode reads the saved notification-address verification record
	// for an address. The Redis adapter returns missing records as errors; profile
	// verification treats missing or unreadable records as an invalid credential.
	// The cache owns expiry-based record availability for that address. The Redis
	// adapter decodes a fresh request record for failed-attempt changes.
	GetNotifyVerifyCode(ctx context.Context, email string) (*VerificationCodeData, error)
	// SetNotifyVerifyCode saves the supplied notification-address challenge record
	// under ttl. Callers select the lifetime; its separate namespace keeps
	// notification checks apart from login-email codes. Encoding or storage
	// failures remain errors for the flow's persistence handling.
	SetNotifyVerifyCode(ctx context.Context, email string, data *VerificationCodeData, ttl time.Duration) error
	// DeleteNotifyVerifyCode removes the address's saved notification-address
	// challenge record. The Redis adapter accepts an already absent key; other
	// cleanup failures remain errors for the credential flow to diagnose or
	// propagate.
	DeleteNotifyVerifyCode(ctx context.Context, email string) error

	// GetPasswordResetToken reads the saved password-reset credential record for
	// an address. The Redis adapter returns missing records as errors; reset
	// verification rejects failed reads, while reset sending can generate a
	// replacement after them. The cache owns expiry-based record availability for
	// that address.
	GetPasswordResetToken(ctx context.Context, email string) (*PasswordResetTokenData, error)
	// SetPasswordResetToken saves the supplied password-reset credential record
	// under ttl. Callers select the lifetime; reset sending saves a new credential
	// before requesting its notification. Encoding or storage failures remain
	// errors for the flow's persistence handling.
	SetPasswordResetToken(ctx context.Context, email string, data *PasswordResetTokenData, ttl time.Duration) error
	// DeletePasswordResetToken removes the address's saved password-reset
	// credential record. The Redis adapter accepts an already absent key; other
	// cleanup failures remain errors for the credential flow to diagnose or
	// propagate.
	DeletePasswordResetToken(ctx context.Context, email string) error

	// IsPasswordResetEmailInCooldown reports whether the address has a recent-send
	// marker. The Redis adapter returns false on lookup failure, allowing the
	// reset queue worker's initial cooldown check to proceed as unmarked.
	IsPasswordResetEmailInCooldown(ctx context.Context, email string) bool
	// SetPasswordResetEmailCooldown stores the address's recent-send marker under
	// ttl. The reset queue flow invokes it after a successful sender result;
	// marker failures remain errors for that flow's diagnostic handling.
	SetPasswordResetEmailCooldown(ctx context.Context, email string, ttl time.Duration) error

	// IncrNotifyCodeUserRate increments the user's notification-code count and
	// refreshes its expiry using window. The Redis adapter performs these as
	// separate commands, returning a completed increment with an error when expiry
	// fails.
	IncrNotifyCodeUserRate(ctx context.Context, userID int64, window time.Duration) (int64, error)
	// GetNotifyCodeUserRate reads the user's saved notification-code request
	// count. Missing or unreadable counts remain cache errors; profile rate
	// checking uses a failed read as no confirmed exceeded limit for that request.
	GetNotifyCodeUserRate(ctx context.Context, userID int64) (int64, error)
}

// VerificationCodeData carries a saved challenge and the state used by
// verification checks.
type VerificationCodeData struct {
	Code string // generated decimal challenge text
	// Attempts is incremented after a mismatching code. Cache persistence can
	// fail, so the current request's count need not be the confirmed stored count.
	Attempts int
	// CreatedAt supplies the send-cooldown reference, while ExpiresAt supplies the
	// remaining lifetime for failed-attempt updates. Issuers may read their clock
	// separately for those timestamps rather than capture one shared instant.
	CreatedAt time.Time // credential creation timestamp
	// ExpiresAt bounds the TTL used when persisting a mismatch. Successful code
	// comparison relies on the cache's read availability instead of this
	// timestamp.
	ExpiresAt time.Time
}

// PasswordResetTokenData carries the cached reset credential and its issuance
// timestamp.
type PasswordResetTokenData struct {
	Token     string    // reset credential token text
	CreatedAt time.Time // credential creation timestamp
}

// FirstEmailLocale returns the first optional locale after trimming its
// whitespace. Empty input returns empty text; a blank first entry stays empty
// even when later entries contain a value, letting the sender choose its
// default language.
func FirstEmailLocale(locales []string) string {
	if len(locales) == 0 {
		return ""
	}
	return strings.TrimSpace(locales[0])
}
