package notification

import (
	"context"
	"time"
)

// RecipientName uses the local part of an email address to address its recipient.
func (s *Mailer) RecipientName(email string) string { return EmailRecipientName(email) }

// SendTeamInvitation sends a team invitation in the recipient's language.
func (s *Mailer) SendTeamInvitation(ctx context.Context, email, recipientName string, recipientUserID int64, teamName, link string, expiresAt time.Time) error {
	return s.SendUserNotification(ctx, SendRequest{
		Event:          NotificationEmailEventTeamInvitation,
		RecipientEmail: email, RecipientName: recipientName, UserID: recipientUserID,
		Variables: map[string]string{"team_name": teamName, "invitation_url": link, "expires_at": expiresAt.Format(time.RFC3339)},
	})
}

// SendOwnershipTransfer sends an ownership-transfer request in the recipient's preferred language.
func (s *Mailer) SendOwnershipTransfer(ctx context.Context, email, teamName, link string) error {
	return s.SendUserNotification(ctx, SendRequest{
		Event:          NotificationEmailEventTeamOwnershipTransfer,
		RecipientEmail: email, RecipientName: EmailRecipientName(email),
		Variables: map[string]string{"team_name": teamName, "transfer_url": link},
	})
}
