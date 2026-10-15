package notification

import (
	"context"
	"time"
)

// team_messages.go sends team invitation and ownership-transfer notifications
// through the shared mailer.
// Team notices carry an invitation or ownership-review link into the shared
// notification renderer. The service resolves recipient language and branding,
// while invitation expiry is represented as an RFC3339 timestamp supplied by
// the team caller.

// RecipientName uses the trimmed local part of an email address for its greeting.
// Blank input returns an empty name, and an input without a usable at-sign
// prefix returns the entire trimmed string through EmailRecipientName.
func (s *Mailer) RecipientName(email string) string { return EmailRecipientName(email) }

// SendTeamInvitation sends a team invitation in the recipient's language.
// RecipientUserID assists language lookup, and expiresAt becomes RFC3339 text.
// The wrapper supplies no delivery source identifier, so notification-service
// source-key deduplication is not selected by this request.
func (s *Mailer) SendTeamInvitation(ctx context.Context, email, recipientName string, recipientUserID int64, teamName, link string, expiresAt time.Time) error {
	// Represent expiry as RFC3339 text, retaining the supplied zone offset for
	// the recipient's displayed invitation deadline before template HTML escaping is applied.
	return s.SendUserNotification(ctx, SendRequest{
		Event:          NotificationEmailEventTeamInvitation,
		RecipientEmail: email, RecipientName: recipientName, UserID: recipientUserID,
		Variables: map[string]string{"team_name": teamName, "invitation_url": link, "expires_at": expiresAt.Format(time.RFC3339)},
	})
}

// SendOwnershipTransfer sends an ownership-transfer request in the recipient's preferred language.
// The email address supplies recipient identity and greeting. The request
// carries team name and review link, and language selection uses the shared
// service's recipient lookup because this wrapper supplies no locale hint.
func (s *Mailer) SendOwnershipTransfer(ctx context.Context, email, teamName, link string) error {
	return s.SendUserNotification(ctx, SendRequest{
		Event:          NotificationEmailEventTeamOwnershipTransfer,
		RecipientEmail: email, RecipientName: EmailRecipientName(email),
		Variables: map[string]string{"team_name": teamName, "transfer_url": link},
	})
}
