package notification

import (
	"context"
	"strconv"
	"time"
)

// identity_messages.go builds localized verification and password-reset
// notification requests from prepared identity credentials.
// Identity prepares verification codes and reset links before asking the mailer
// to render a localized event. Expiry durations here populate human-readable
// template variables; credential storage, cooldowns, and actual token lifetime
// remain defined by the identity challenge implementation.

// Identity expiry labels describe the verification and reset durations displayed
// in notification templates; the mailer converts these durations into minute text.
const (
	verifyCodeTTL         = 15 * time.Minute
	passwordResetTokenTTL = 30 * time.Minute
)

// SendVerifyCodeMessage sends a verification code using the selected locale's template.
// It forwards the prepared code and displayed expiry to SendUserNotification.
// siteName is accepted for the identity sender interface; notification rendering
// obtains current branding from stored settings through its service.
func (s *Mailer) SendVerifyCodeMessage(ctx context.Context, email, siteName, code, language string) error {
	// Express the displayed verification lifetime in whole minutes.
	return s.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventAuthVerifyCode, Locale: language,
		RecipientEmail: email, RecipientName: EmailRecipientName(email),
		Variables: map[string]string{"verification_code": code, "expires_in_minutes": strconv.Itoa(int(verifyCodeTTL / time.Minute))},
	})
}

// SendPasswordResetMessage renders a reset link through the localized template's variable escaping.
// The supplied link and locale are forwarded to the notification service.
// siteName is an interface input; current template branding is resolved from
// settings, and this wrapper performs no token generation or credential storage.
func (s *Mailer) SendPasswordResetMessage(ctx context.Context, email, siteName, fullResetURL, language string) error {
	// Express the displayed reset-link lifetime in whole minutes.
	return s.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventAuthPasswordReset, Locale: language,
		RecipientEmail: email, RecipientName: EmailRecipientName(email),
		Variables: map[string]string{"reset_url": fullResetURL, "expires_in_minutes": strconv.Itoa(int(passwordResetTokenTTL / time.Minute))},
	})
}

// SendNotifyVerification sends a notification-address verification code in the user's language.
// UserID participates in recipient-language lookup, while the prepared code
// and displayed duration become template variables. siteName is an interface
// input and current branding is resolved by the notification service.
func (s *Mailer) SendNotifyVerification(ctx context.Context, userID int64, email, code, language, siteName string) error {
	// Notification-address verification uses the displayed verification-minute lifetime.
	return s.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventNotificationEmailVerifyCode, Locale: language,
		RecipientEmail: email, RecipientName: EmailRecipientName(email), UserID: userID,
		Variables: map[string]string{"verification_code": code, "expires_in_minutes": strconv.Itoa(int(verifyCodeTTL / time.Minute))},
	})
}
