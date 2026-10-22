package app

import (
	"context"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// provider_balance_alert.go binds periodic wallet monitoring to notification
// templates and the application lifecycle. The provider module chooses when a
// reminder is due and which balance crossed its threshold. Notification receives
// presentation variables and one recipient, and its SMTP outcome is returned to
// the monitor for the latest-attempt status.

// provideBalanceAlertMonitor binds durable claims, shared usage queries, and email delivery.
// The monitor starts after assembly and stops before the upstream query service
// and shared dependencies. SQL claims own recurring delivery identity, so this
// sender omits notification source markers rather than allocating daily or
// per-round settings keys. Each SMTP invocation belongs to one consumed round;
// failures propagate for status recording and do not trigger transport retries.
func provideBalanceAlertMonitor(store *providerpostgres.ProviderStore, queries *provider.UpstreamUsageService, mail *notification.NotificationEmailService, manager *lifecycle.Manager) *provider.BalanceAlertMonitor {
	send := func(ctx context.Context, p *provider.Record, c provider.BalanceAlertConfig, result *provider.UpstreamUsageQueryResult) error {
		return mail.Send(ctx, notification.SendRequest{
			Event:          notification.NotificationEmailEventProviderUpstreamBalanceLow,
			RecipientEmail: c.RecipientEmail,
			RecipientName:  notification.EmailRecipientName(c.RecipientEmail),
			Variables: map[string]string{
				"provider_id":   strconv.FormatInt(p.ID, 10),
				"provider_name": p.Name,
				"balance":       strconv.FormatFloat(*result.Balance.Remaining, 'f', -1, 64),
				"threshold":     strconv.FormatFloat(c.Threshold, 'f', -1, 64),
				"unit":          result.Unit,
				"observed_at":   result.ObservedAt.UTC().Format(time.RFC3339),
			},
		})
	}
	monitor := provider.NewBalanceAlertMonitor(store, queries, send)
	manager.Register(lifecycle.Hook{Name: "ProviderBalanceAlerts", StartOrder: 980, StopOrder: shutdownStageProducers, Start: monitor.StartContext, Stop: monitor.StopContext})
	return monitor
}
