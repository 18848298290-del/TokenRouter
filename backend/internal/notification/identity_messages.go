package notification

import (
	"context"
	"strconv"
	"time"
)

const (
	verifyCodeTTL         = 15 * time.Minute
	passwordResetTokenTTL = 30 * time.Minute
)

// SendVerifyCodeMessage sends a verification code using the selected locale's template.
func (s *Mailer) SendVerifyCodeMessage(ctx context.Context, email, siteName, code, language string) error {
	return s.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventAuthVerifyCode, Locale: language,
		RecipientEmail: email, RecipientName: EmailRecipientName(email),
		Variables: map[string]string{"verification_code": code, "expires_in_minutes": strconv.Itoa(int(verifyCodeTTL / time.Minute))},
	})
}

// SendPasswordResetMessage renders a reset link through the localized template's variable escaping.
func (s *Mailer) SendPasswordResetMessage(ctx context.Context, email, siteName, fullResetURL, language string) error {
	return s.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventAuthPasswordReset, Locale: language,
		RecipientEmail: email, RecipientName: EmailRecipientName(email),
		Variables: map[string]string{"reset_url": fullResetURL, "expires_in_minutes": strconv.Itoa(int(passwordResetTokenTTL / time.Minute))},
	})
}

// SendNotifyVerification sends a notification-address verification code in the user's language.
func (s *Mailer) SendNotifyVerification(ctx context.Context, userID int64, email, code, language, siteName string) error {
	return s.SendUserNotification(ctx, SendRequest{
		Event: NotificationEmailEventNotificationEmailVerifyCode, Locale: language,
		RecipientEmail: email, RecipientName: EmailRecipientName(email), UserID: userID,
		Variables: map[string]string{"verification_code": code, "expires_in_minutes": strconv.Itoa(int(verifyCodeTTL / time.Minute))},
	})
}
