package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Billing reminder readiness distinguishes unconfigured SMTP from a settings
// read failure. An empty settings snapshot exercises the notification sentinel
// mapping, while an injected repository error checks the original failure
// remains available to the reminder worker.

// expirySettingsFixture supplies empty mail settings or a configured repository
// error.
type expirySettingsFixture struct {
	// Repository supplies unused methods; GetMultiple implements the settings
	// snapshot consumed by notification transport readiness in these two cases.
	settings.Repository
	err error // injected settings failure
}

// GetMultiple returns an empty settings snapshot with the configured fixture
// error.
func (r expirySettingsFixture) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, r.err
}

// TestExpiryNotificationReadinessBridge checks empty SMTP settings and a failed
// settings read through the reminder adapter. It expects billing's unconfigured
// transport sentinel for absence and the original repository error for failure.
func TestExpiryNotificationReadinessBridge(t *testing.T) {
	for _, failure := range []error{nil, errors.New("db down")} {
		repo := expirySettingsFixture{err: failure}
		mailer := notification.NewMailer(repo, smtp.New())
		sender := notification.NewNotificationEmailService(repo, mailer)
		mailer.SetNotificationEmailService(sender)
		adapter := expiryNotifications{Service: sender}
		err := adapter.Ready(context.Background())
		if failure == nil {
			require.ErrorIs(t, err, billing.ErrReminderTransportUnconfigured)
		} else {
			require.ErrorIs(t, err, failure)
		}
	}
}
