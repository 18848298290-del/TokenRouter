package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	identityredis "github.com/TokenFlux/TokenRouter/internal/identity/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	notificationhttp "github.com/TokenFlux/TokenRouter/internal/notification/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Identity challenges own verification codes while notification owns templates
// and SMTP delivery. Application assembly binds the mailer back to the same
// notification service, adds recipient-locale lookup from users, and adapts
// subscription reminder readiness and localized plan text for billing.

// billingQuotaNotifyReader adapts stored provider data for billing quota
// notifications.
type billingQuotaNotifyReader struct {
	providers *providerpostgres.ProviderStore // provider quota reads
}

// expiryNotifications converts billing reminders to localized notification
// service requests.
type expiryNotifications struct {
	Service *notification.NotificationEmailService // subscription reminder delivery
}

// provideEmailCache binds identity email challenge storage to the shared Redis
// client.
func provideEmailCache(r *redis.Client) identity.EmailCache { return identityredis.NewEmailCache(r) }

// provideMailer constructs notification mail delivery using settings and SMTP
// transport.
func provideMailer(store *settings.Store) *notification.Mailer {
	return notification.NewMailer(store, smtp.New())
}

// provideEmailChallenges combines identity challenge storage and prepared email
// delivery.
func provideEmailChallenges(cache identity.EmailCache, mail *notification.Mailer) *identity.EmailChallenges {
	return identity.NewEmailChallenges(cache, mail)
}

// provideNotification constructs templated delivery and installs recipient
// account-locale lookup. Positive IDs select user lookup; other recipients use
// email lookup, and read failures or absent preferences yield an empty locale
// for notification fallback. The mailer receives this same service before
// assembly returns.
func provideNotification(store *settings.Store, mail *notification.Mailer, users *identitypostgres.UserStore) *notification.NotificationEmailService {
	n := notification.NewNotificationEmailService(store, mail)
	n.SetRecipientLocaleReader(func(ctx context.Context, id int64, email string) string {
		var user *identity.User
		var err error
		if id > 0 {
			user, err = users.GetByID(ctx, id)
		} else {
			user, err = users.GetByEmail(ctx, email)
		}
		if err == nil && user != nil && user.PreferredLocale != nil {
			return *user.PreferredLocale
		}
		// Account lookup failure leaves the preferred locale unset. Notification
		// then continues its own language selection through legacy, remembered-email, and site settings.
		return ""
	})
	// Mailer and templates call each other through this assembled service.
	// Bind it before returning so identity delivery and business templates share one notification runtime.
	mail.SetNotificationEmailService(n)
	return n
}

// provideEmailQueue creates the identity email queue with three workers.
// Lifecycle Start runs the workers separately, and callers stop producers
// before draining this queue through its registered stop hook.
func provideEmailQueue(c *identity.EmailChallenges) *notification.EmailQueueService {
	return notification.NewEmailQueueService(c, 3)
}

// provideNotificationHTTP binds notification administration and public
// unsubscribe to mail and display settings.
func provideNotificationHTTP(mail *notification.Mailer, n *notification.NotificationEmailService, settings *site.DisplaySettings) *notificationhttp.Handler {
	return notificationhttp.New(mail, n, settings)
}

// notificationOpsDelivery converts Ops email inputs into notification requests
// and locale reads. TemplatesEnabled records service presence, while the send
// callback delegates all event, source, reminder, and variable fields to that
// service.
func notificationOpsDelivery(mail *notification.Mailer, n *notification.NotificationEmailService) *ops.EmailDelivery {
	return &ops.EmailDelivery{TemplatesEnabled: n != nil, SendEmail: mail.SendEmail, RecipientName: notification.EmailRecipientName, ShouldFallback: notification.ShouldFallbackNotificationEmail, ResolveRecipientLocale: n.ResolveRecipientLocale, SendTemplate: func(ctx context.Context, v ops.Notification) error {
		return n.Send(ctx, notification.SendRequest{Event: v.Event, Locale: v.Locale, RecipientEmail: v.RecipientEmail, RecipientName: v.RecipientName, SourceType: v.SourceType, SourceID: v.SourceID, ReminderKey: v.ReminderKey, Variables: v.Variables, RawHTMLVariables: v.RawHTMLVariables})
	}}
}

// provideAlertDelivery constructs prepared alert delivery from mail and
// template services.
func provideAlertDelivery(mail *notification.Mailer, n *notification.NotificationEmailService) *notification.AlertDelivery {
	return notification.NewAlertDelivery(mail, n)
}

// provideBalanceNotifications combines balance and provider quota notices with
// tracked background work.
func provideBalanceNotifications(sender *notification.AlertDelivery, settings *settings.Store, providers *providerpostgres.ProviderStore, tasks *lifecycle.Tasks) *billing.BalanceNotifyService {
	return billing.NewBalanceNotifyService(sender, settings, billingQuotaNotifyReader{providers}, func(name string, fn func()) { tasks.Go(name, fn) })
}

// GetByID converts stored provider data into the billing quota notice view.
func (r billingQuotaNotifyReader) GetByID(ctx context.Context, id int64) (*billing.QuotaNotifyProvider, error) {
	a, e := r.providers.GetByID(ctx, id)
	return provideradapter.QuotaNotification(a), e
}

// Ready checks reminder transport through notification and maps unconfigured
// email to the billing reminder error. A nil service returns that same
// unconfigured result, while other transport errors remain available to the
// reminder worker.
func (n expiryNotifications) Ready(ctx context.Context) error {
	if n.Service == nil {
		return billing.ErrReminderTransportUnconfigured
	}
	err := n.Service.CheckTransport(ctx)
	// Billing distinguishes an absent reminder transport from transient delivery
	// failures. Map the notification sentinel while retaining unrelated settings and SMTP errors.
	if errors.Is(err, notification.ErrEmailNotConfigured) {
		return billing.ErrReminderTransportUnconfigured
	}
	return err
}

// Send resolves recipient language and sends the billing subscription reminder.
// The worker checks Ready before sending; versioned plan localization replaces
// PlanName for that language, and subscription ID plus days remaining form the
// notification source and reminder keys.
func (n expiryNotifications) Send(ctx context.Context, r billing.ExpiryReminder) error {
	language := n.Service.ResolveRecipientLocale(ctx, r.UserID, r.RecipientEmail)
	if r.PlanLocalization.Revision > 0 {
		copy, _ := locale.Content[billing.PlanCopy](r.PlanLocalization).Resolve(language)
		r.PlanName = copy.Name
	}
	return n.Service.Send(ctx, notification.SendRequest{
		Locale:         language,
		Event:          notification.NotificationEmailEventSubscriptionExpiryReminder,
		RecipientEmail: r.RecipientEmail, RecipientName: r.RecipientName, UserID: r.UserID,
		SourceType: "user_subscription", SourceID: strconv.FormatInt(r.SubscriptionID, 10),
		ReminderKey: fmt.Sprintf("%dd", r.DaysRemaining),
		Variables:   map[string]string{"subscription_group": r.PlanName, "expiry_time": r.ExpiresAt.Format("2006-01-02 15:04"), "days_remaining": strconv.Itoa(r.DaysRemaining)},
	})
}
