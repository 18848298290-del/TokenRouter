package ops

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// runtime_ports.go defines cache, locking, notification, and cleanup
// dependencies used by Ops background services.
// Background Ops jobs coordinate through named cache leases, database lock
// attempts, and the shared pre-aggregation settings reader. Each job chooses
// its recovery policy when storage is unavailable. Email callbacks connect
// alert and report data to the application notification service and plain-mail
// sender.

// RuntimeCache supplies caller-named leases and string values for Ops
// background tasks.
type RuntimeCache interface {
	// Claim attempts to store owner under key for the supplied lease lifetime when
	// the key is absent. False reports contention; errors leave acquisition
	// uncertain under the configured recovery policy.
	Claim(context.Context, string, string, time.Duration) (bool, error)
	// Release attempts to remove key when its stored owner matches the supplied
	// owner. A missing key or different owner is a successful no-op; callers still
	// handle any storage error.
	Release(context.Context, string, string) error
	// Get reads the string stored under the caller-supplied key for Ops task
	// state. Missing values are reported by the adapter as errors, and callers
	// select their own missing-value policy.
	Get(context.Context, string) (string, error)
	// Put replaces the string stored under key using the supplied expiration
	// duration. Ops callers choose key names and lifetime; storage errors remain
	// available for each caller to handle.
	Put(context.Context, string, string, time.Duration) error
}

// AdvisoryLocker supplies a database lock attempt and its successful
// acquisition cleanup.
type AdvisoryLocker interface {
	// Acquire attempts the named advisory lock and returns release with true after
	// acquisition. Call release once after the job completes; false can represent
	// contention or a database acquisition failure.
	Acquire(context.Context, string) (func(), bool)
}

// PreAggregationRuntimeReader supplies the shared Ops aggregation switch and
// setting-change callbacks.
type PreAggregationRuntimeReader interface {
	// OpsEnabled resolves whether deployment availability and the shared runtime
	// setting permit Ops aggregation. The shared settings reader applies its cache
	// or deployment-default policy on read errors and returns a boolean decision.
	OpsEnabled(context.Context) bool
	// RegisterListener retains a callback receiving previous and next
	// pre-aggregation settings when the shared reader publishes a change. The
	// aggregation service registers during construction and uses enable
	// transitions to request prompt runs.
	RegisterListener(func(preaggregation.PreAggregationSettings, preaggregation.PreAggregationSettings))
}

// ProxyStatsReader supplies proxy-expiry observations used by the Ops alert
// evaluator.
type ProxyStatsReader interface {
	// CountExpired returns the number of undeleted proxies carrying expired status
	// for evaluator metrics. Query errors cause the evaluator to skip this metric
	// during the current cycle.
	CountExpired(context.Context) (int64, error)
	// CountExpiringSoon counts active, undeleted proxies whose expiry follows the
	// supplied time and falls within their warning period. Errors make the
	// evaluator metric unavailable for that cycle.
	CountExpiringSoon(context.Context, time.Time) (int64, error)
}

// AdminReader supplies an administrator identity for scheduled-report recipient
// selection.
type AdminReader interface {
	// GetFirstAdmin reads an administrator identity used when a report has no
	// configured recipients. A failed lookup or an empty returned email leaves the
	// report with no fallback recipient.
	GetFirstAdmin(context.Context) (*UserObservation, error)
}

// EmailDelivery contains notification and plain-email callbacks shared by
// alerts and scheduled reports.
type EmailDelivery struct {
	// TemplatesEnabled selects notification-template delivery before plain email.
	// Set the template, name, locale, and recovery callbacks before starting
	// services that enable this path.
	TemplatesEnabled bool
	// SendEmail attempts delivery using context, recipient address, subject, and
	// HTML body. Supply it for plain-email delivery and eligible recovery after a
	// template failure.
	SendEmail func(context.Context, string, string, string) error
	// SendTemplate attempts delivery of the supplied event and recipient metadata.
	// Alerts and reports call it when TemplatesEnabled selects
	// notification-template delivery.
	SendTemplate func(context.Context, Notification) error
	// ResolveRecipientLocale chooses report language from a user ID and email
	// address. Scheduled reports pass zero as the user ID and provide the
	// recipient address.
	ResolveRecipientLocale func(context.Context, int64, string) string
	// RecipientName derives the name supplied to notification-template delivery
	// for an email address. Template-enabled alerts and reports call it before
	// invoking SendTemplate.
	RecipientName func(string) string
	// ShouldFallback decides whether a template failure permits plain-email
	// delivery. False skips that recipient, and the calling report or alert
	// continues its recipient loop.
	ShouldFallback func(error) bool
}

// Notification carries Ops event identity and variables to the notification
// email service.
type Notification struct {
	// Event names the notification template, Locale selects its language,
	// RecipientEmail identifies the address, and RecipientName supplies its
	// display name. SourceType identifies the producer, SourceID identifies its
	// report or alert, and ReminderKey distinguishes a scheduled report minute for
	// notification deduplication.
	Event, Locale, RecipientEmail, RecipientName, SourceType, SourceID, ReminderKey string
	// Variables holds ordinary template substitutions, and RawHTMLVariables holds
	// HTML fragments supplied by report generation. Both maps are passed to the
	// notification callback; callers keep them stable while that callback reads
	// them.
	Variables, RawHTMLVariables map[string]string
}

// reportOptions writes a formatted Ops diagnostic through the injected process
// logger. Nil options or a missing callback discard the diagnostic, so callers
// can report optional dependency failures without requiring a logger.
func reportOptions(o *Options, format string, args ...any) {
	if o != nil && o.Logf != nil {
		o.Logf(format, args...)
	}
}

// acquireAdvisory delegates a named database lock attempt or reports
// unavailable locking for a nil dependency. On success, the caller invokes the
// returned release function after its protected work finishes.
func acquireAdvisory(ctx context.Context, p AdvisoryLocker, key string) (func(), bool) {
	if p == nil {
		return nil, false
	}
	return p.Acquire(ctx, key)
}
