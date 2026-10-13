package team

import (
	"context"
	"strings"
	"time"
)

// Invitation greetings begin with the sender's mailbox-derived name, then a
// usable account lookup can supply a trimmed username and recipient identity.
// Lookup failure keeps the initial greeting and zero identity so configured
// mail delivery can still proceed.
//
// Sender absence makes invitation delivery a successful skip before link
// validation, while configured senders need a nonblank review link.
// Ownership-transfer delivery delegates directly and therefore requires a
// sender. The adapter passes the invitation expiry to template delivery
// rather than enforcing token lifetime itself; token persistence and
// mail-failure consequences remain with TeamService and its repository.
// Notification consumers select locale from the recipient inputs they
// receive.

// InvitationRecipientReader supplies optional account display data for
// invitation email recipients.
type InvitationRecipientReader interface {
	// GetByEmail looks up display and account identity for the supplied
	// invitation mailbox. SendInvitation ignores lookup failures or absent
	// results, retaining its sender-derived greeting and zero recipient
	// identity.
	GetByEmail(context.Context, string) (*UserSnapshot, error)
}

// TeamMailSender renders and delivers the team invitation and
// ownership-transfer messages.
type TeamMailSender interface {
	// RecipientName returns the sender's initial greeting name for a mailbox.
	RecipientName(string) string
	// SendTeamInvitation delivers a team invitation using the supplied greeting,
	// recipient ID, team name, link and expiry. Zero recipient identity permits
	// mailbox-based notification defaults; delivery failures remain errors for
	// the service caller.
	SendTeamInvitation(context.Context, string, string, int64, string, string, time.Time) error
	// SendOwnershipTransfer delivers the team ownership review request to its
	// mailbox. The caller supplies team name and link, while the sender owns
	// template/locale selection and reports delivery failure.
	SendOwnershipTransfer(context.Context, string, string, string) error
}

// EmailNotifications resolves invitation greetings before delegating
// configured team mail delivery.
type EmailNotifications struct {
	// sender is retained for team mail delivery. Nil skips SendInvitation before
	// validation, while SendOwnershipTransfer requires this capability to be
	// present.
	sender TeamMailSender
	// users optionally supplies recipient identity and username for invitation
	// delivery. Lookup failures or absent accounts retain the sender-derived
	// name and zero identity.
	users InvitationRecipientReader
}

// NewEmailNotifications retains the sender and optional recipient reader for
// team mail delivery. Nil sender disables invitation sends;
// ownership-transfer callers supply a sender before invoking that directly
// delegated operation.
func NewEmailNotifications(sender TeamMailSender, users InvitationRecipientReader) *EmailNotifications {
	return &EmailNotifications{sender: sender, users: users}
}

// SendInvitation resolves recipient greeting/identity and delegates a
// configured invitation message. Nil sender returns success before link
// validation; otherwise blank link is unavailable and sender-derived greeting
// provides the initial name.
//
// A successful nonnil account lookup supplies its ID as returned and a
// nonblank trimmed username can replace the greeting independently. Lookup
// failure keeps the initial name and zero ID. Delivery receives the supplied
// expiry and its errors remain visible; this adapter neither stores tokens
// nor compensates the repository writes performed by TeamService.
func (s EmailNotifications) SendInvitation(ctx context.Context, email, teamName, link string, expiresAt time.Time) error {
	if s.sender == nil {
		return nil
	}
	if strings.TrimSpace(link) == "" {
		return ErrTeamFrontendURLUnavailable
	}
	// Prepare the sender-derived greeting before optional account lookup,
	// retaining a usable invitation greeting and zero recipient identity when
	// lookup fails or no account exists.
	recipientName := s.sender.RecipientName(email)
	var recipientUserID int64
	if s.users != nil {
		if user, err := s.users.GetByEmail(ctx, email); err == nil && user != nil {
			recipientUserID = user.ID
			if strings.TrimSpace(user.Username) != "" {
				recipientName = strings.TrimSpace(user.Username)
			}
		}
	}

	return s.sender.SendTeamInvitation(ctx, email, recipientName, recipientUserID, teamName, link, expiresAt)
}

// SendOwnershipTransfer delegates the supplied mailbox, team name and review
// link to the retained sender. A sender is required for this operation;
// template/locale decisions and delivery failures belong to that capability.
func (s EmailNotifications) SendOwnershipTransfer(ctx context.Context, email, name, link string) error {
	return s.sender.SendOwnershipTransfer(ctx, email, name, link)
}
