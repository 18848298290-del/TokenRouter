package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// subscription_expiry.go runs subscription expiry maintenance and dispatches
// due reminder requests.
// Subscription maintenance first marks elapsed active/pending grants expired,
// then scans active rows for reminders at rounded seven-, three- and one-day
// thresholds. Settings and notifier readiness can skip the reminder scan
// without reversing a successful status update. Notification identity and
// successful-delivery deduplication belong to the injected notifier, so this
// scanner may submit the same due reminder on successive cycles.
//
// The worker starts immediately and repeats on its configured interval;
// cancellation propagates into independent status, page and send contexts. A
// send timeout does not become the next page's context. A lease covers the
// paginated scan without renewal here, and an accepted lease returns cleanup
// ownership to this service. Stop seals restart and joins the worker within
// the caller's wait budget; a timed-out wait can leave uncooperative work
// running.

const (
	// Operation budgets bound cooperative status, setting/page, send and lease
	// work independently. A separate warning interval throttles
	// unconfigured-transport observations while the paginated lease has its own
	// lifetime.
	subscriptionExpiryUpdateTimeout               = 10 * time.Second                      // status update operation budget
	subscriptionExpiryReminderListTimeout         = 10 * time.Second                      // settings lease and page budget
	subscriptionExpiryReminderSendTimeout         = 30 * time.Second                      // individual reminder send budget
	subscriptionExpiryReminderSMTPWarningInterval = time.Minute                           // unconfigured transport warning interval
	subscriptionExpiryReminderLeaderLockKey       = "subscription:expiry:reminder:leader" // shared subscription reminder lease key
	// The requested lease lifetime applies to the whole scan, independently of
	// each call budget. It can expire while pages or sends continue because this
	// scanner does not renew it.
	subscriptionExpiryReminderLeaderLockTTL = 5 * time.Minute // paginated reminder lease lifetime
)

// ErrReminderTransportUnconfigured distinguishes absent delivery configuration
// from other readiness failures, allowing the scanner to throttle this
// specific warning while skipping reminder work.
var ErrReminderTransportUnconfigured = errors.New("subscription reminder transport is not configured")

// ExpiryReminder carries a selected grant deadline, recipient and plan content
// for notification delivery.
type ExpiryReminder struct {
	// UserID and SubscriptionID identify the recipient owner and specific grant
	// together. The app notifier uses them for locale lookup and source-backed
	// delivery identity, keeping distinct grants' reminder keys separate.
	// UserID: paying subscription owner identifier.
	// SubscriptionID: reminder source grant identifier.
	UserID, SubscriptionID int64
	// RecipientEmail, RecipientName and PlanName contain the selected delivery
	// addresses/display text. App notification can resolve localized plan content
	// from PlanLocalization before formatting the message.
	// RecipientEmail: selected recipient email address.
	// RecipientName: selected recipient display name.
	// PlanName: selected plan display text.
	RecipientEmail, RecipientName, PlanName string
	ExpiresAt                               time.Time // selected grant expiry timestamp
	// DaysRemaining is the upward-rounded twenty-four-hour lifetime count
	// selected at seven, three or one. The app notifier uses this number in its
	// reminder key and template variables.
	DaysRemaining int // rounded remaining lifetime days
	// PlanLocalization is copied from the loaded plan when present. App delivery
	// resolves a positive revision for the recipient's language, potentially
	// replacing PlanName; nested content sharing follows that value type.
	PlanLocalization PlanLocalization // optional current plan localization content
}

// ExpiryNotifier checks reminder transport readiness and delivers
// caller-selected subscription events.
type ExpiryNotifier interface {
	// Ready reports whether reminder transport is currently usable under the
	// supplied context. ErrReminderTransportUnconfigured selects throttled
	// warning handling; other errors skip this scan with ordinary failure
	// observation.
	Ready(context.Context) error
	// Send attempts delivery of the already selected reminder within the supplied
	// operation context. The notifier owns recipient language, templates and
	// source-backed delivery deduplication; the scanner observes errors and
	// continues later eligible rows.
	Send(context.Context, ExpiryReminder) error
}

// ExpirySettings supplies the optional persisted subscription-reminder enable
// switch for the scanner.
type ExpirySettings interface {
	// GetValue reads the reminder switch text for the supplied key and context. A
	// setting-not-found error enables reminders by default, while other read
	// errors disable the current scan and are observed.
	GetValue(context.Context, string) (string, error)
}

// ExpiryLease attempts scan ownership using key, owner and TTL and returns
// release plus acquisition status. Successful acquisition supplies a nonnil
// release callable after acquisition context cancellation; false causes
// scanning to skip this cycle.
type ExpiryLease func(context.Context, string, string, time.Duration) (func(), bool)

// ExpiryOptions supplies unstarted maintenance timing, callbacks and optional
// reminder dependencies.
type ExpiryOptions struct {
	// Interval is retained as supplied. Start requires a positive duration and
	// usable repository; nonpositive intervals keep the constructed service
	// unstarted rather than choosing a repeat default.
	Interval time.Duration // worker scan repeat cadence
	Owner    string        // caller-provided reminder lease owner
	// Now controls rounded days and SMTP warning timing. The constructor defaults
	// nil to time.Now; repository expiry/list predicates still use their own
	// clock observations.
	Now func() time.Time // reminder observation clock callback
	// Observe receives worker and reminder diagnostics and defaults to a no-op.
	// It must return without reentering guarded warning handling, which invokes
	// observation while smtpWarningMu is held.
	Observe func(string, ...any) // maintenance observation logging callback
	// Settings omission enables reminders by default. Missing switch values also
	// enable them, while other read errors skip notification scanning for the
	// current cycle.
	Settings ExpirySettings // optional reminder switch reader
	// Notifier omission skips reminder scanning but leaves expired-status
	// maintenance available. A configured notifier must supply readiness and
	// cooperative send behavior for the selected operation budgets.
	Notifier ExpiryNotifier // optional reminder delivery dependency
	// Lease omission selects a successful no-op release for standalone execution.
	// App wiring injects singleton ownership; acquisition failure skips scanning,
	// and release must remain callable after its acquire context is canceled.
	Lease ExpiryLease // optional paginated scan lease function
}

// SubscriptionExpiryService coordinates periodic status maintenance, reminders
// and one worker lifetime. Construct it with NewSubscriptionExpiryService
// before stopping; a zero-valued service lacks the completion channel required
// by StopContext.
type SubscriptionExpiryService struct {
	// userSubRepo is retained from construction. Nil prevents Start; direct cycle
	// work requires a usable repository for status updates and reminder pages.
	userSubRepo UserSubscriptionRepository // grant status and page repository
	// settingRepo may be nil for default-enabled reminders. Configured nonmissing
	// read failures skip the current reminder scan while leaving status
	// maintenance separate.
	settingRepo ExpirySettings // optional reminder switch repository
	// notificationEmailService may be nil to skip reminders. Its readiness and
	// Send callbacks run in operation contexts and cooperate with worker
	// cancellation to finish shutdown.
	notificationEmailService ExpiryNotifier // optional reminder transport notifier
	// interval, updateTimeout, reminderListTimeout and reminderSendTimeout
	// separate repeat cadence from status, settings/page/lease and individual
	// send budgets. Timeout accessors replace nonpositive stored budgets with
	// their fixed defaults.
	// interval: worker scan repeat cadence.
	// updateTimeout: status update operation budget.
	// reminderListTimeout: settings lease/page operation budget.
	// reminderSendTimeout: individual reminder operation budget.
	interval, updateTimeout, reminderListTimeout, reminderSendTimeout time.Duration
	instanceID                                                        string // retained scan lease owner
	// now, observe and lease retain constructor-normalized callbacks for later
	// scans. Callers keep their closure state usable throughout worker activity,
	// including lease cleanup and locked warning observation.
	now func() time.Time // retained reminder observation clock
	// observe runs during status and reminder diagnostics, including under
	// smtpWarningMu. Callback state remains usable throughout worker activity and
	// avoids reentering guarded warning handling.
	observe func(string, ...any) // retained maintenance observation callback
	// lease is invoked with an independently budgeted acquisition context that is
	// canceled before scanning. Successful release remains usable throughout the
	// scan and its deferred cleanup.
	lease ExpiryLease // retained scan ownership function
	// mu guards started, stopped, cancel and done publication as one worker
	// lifetime. Construction initializes done; StopContext closes it for an
	// unstarted service or waits for the running worker to close it.
	// started: first worker launch accepted.
	// stopped: worker restart permanently sealed.
	mu               sync.Mutex // worker lifetime transition mutex
	started, stopped bool
	cancel           context.CancelFunc // started worker cancellation function
	// done publishes actual worker completion. stopped can already be true while
	// this channel remains open after a caller-budget timeout, so later joins
	// still wait here.
	done chan struct{} // worker completion publication channel
	// smtpWarningMu and lastSMTPWarning serialize unconfigured warnings using the
	// injected clock. Observer invocation occurs while locked, so callbacks avoid
	// reentry and keep warning handling bounded by their own cooperation.
	smtpWarningMu sync.Mutex // unconfigured transport warning mutex
	// lastSMTPWarning uses zero as the unissued-warning marker. An injected zero
	// clock value remains that marker after logging, allowing another immediate
	// observation.
	lastSMTPWarning time.Time // last unconfigured transport observation time
}

// NewSubscriptionExpiryService initializes an unstarted worker and fixed
// operation budgets from supplied options. Nil clock, observer and lease
// receive wall time, no-op observation and successful no-op release
// respectively; interval and repository remain caller-supplied and control
// Start eligibility.
func NewSubscriptionExpiryService(repo UserSubscriptionRepository, options ExpiryOptions) *SubscriptionExpiryService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Observe == nil {
		options.Observe = func(string, ...any) {}
	}
	if options.Lease == nil {
		options.Lease = func(context.Context, string, string, time.Duration) (func(), bool) { return func() {}, true }
	}
	return &SubscriptionExpiryService{userSubRepo: repo, settingRepo: options.Settings, notificationEmailService: options.Notifier, interval: options.Interval, updateTimeout: subscriptionExpiryUpdateTimeout, reminderListTimeout: subscriptionExpiryReminderListTimeout, reminderSendTimeout: subscriptionExpiryReminderSendTimeout, instanceID: options.Owner, now: options.Now, observe: options.Observe, lease: options.Lease, done: make(chan struct{})}
}

// Start launches one background worker when repository and positive interval
// are configured, tolerating a nil service. Started/stopped transitions share
// the mutex, and a stopped service cannot restart. Construction must
// initialize done before this lifetime begins.
//
// The worker runs its first cycle before waiting on the ticker and derives
// each cycle's work from one cancellable lifetime context. Deferred completion
// closes done after that worker returns, while ticker cleanup stops further
// ticks. Cancellation is checked before another tick-triggered cycle;
// callbacks already running must cooperate for completion.
func (s *SubscriptionExpiryService) Start() {
	if s == nil {
		return
	}
	// Serialize worker admission with stopping so only a started worker owns
	// completion closure; stopped or unconfigured lifetimes never launch another
	// ticker.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped || s.userSubRepo == nil || s.interval <= 0 {
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	// Run the immediate scan before periodic ticks, and publish done only after
	// all worker-return cleanup; StopContext waits on this worker-owned
	// completion signal.
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		s.runOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				s.runOnce(ctx)
			}
		}
	}()
}

// StopContext permanently seals restart, cancels a started worker or publishes
// completion for an unstarted service, and waits within caller context. Nil
// service returns nil; repeated callers share the same done signal but retain
// their own wait budgets. Work already complete at the first nonblocking check
// returns nil before inspecting caller cancellation; a later completion racing
// context expiry uses the selected ready case.
//
// A wait timeout returns that context error while the worker can still be
// draining or blocked in an uncooperative dependency. The running worker owns
// closing done; an unstarted stop closes it under the lifetime mutex. Caller
// cancellation bounds this wait, not a guarantee that all work has already
// returned.
func (s *SubscriptionExpiryService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	// Seal restart and choose cancellation versus direct completion closure under
	// one mutex, preventing repeated stops from closing the same signal
	// independently.
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		if s.started {
			s.cancel()
		} else {
			close(s.done)
		}
	}
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop seals and joins the service using a background wait context, tolerating
// nil. It discards StopContext's result and provides no caller deadline;
// dependency cooperation therefore controls how long this convenience wrapper
// can wait.
func (s *SubscriptionExpiryService) Stop() {
	// Use an unbounded wait context for this compatibility wrapper and discard
	// its result; timed lifecycle shutdown calls StopContext directly with a
	// caller budget.
	_ = s.StopContext(context.Background())
}

// runOnce updates expired grant statuses with its own parent-derived budget,
// then scans reminders after successful update. Update failure is observed and
// stops this cycle; a positive count is observed before notifications.
//
// The update context is canceled after its call rather than reused for
// reminder work. Consequently status timeout and send/page budgets have
// separate lifetimes while still inheriting worker cancellation. Reminder
// failure cannot reverse an already successful status update, and this cycle
// delegates query semantics to its repository.
func (s *SubscriptionExpiryService) runOnce(parent context.Context) {
	// Give status mutation a separate parent-derived operation budget, then
	// cancel that context before any reminder operation creates its own context.
	ctx, cancel := context.WithTimeout(parent, s.expiredStatusUpdateTimeout())
	updated, err := s.userSubRepo.BatchUpdateExpiredStatus(ctx)
	cancel()
	if err != nil {
		// Stop this cycle before its reminder phase when status maintenance fails. A
		// later periodic cycle begins with a fresh status budget and can attempt the
		// update again.
		s.observe("[SubscriptionExpiry] Update expired subscriptions failed: %v", err)
		return
	}
	if updated > 0 {
		s.observe("[SubscriptionExpiry] Updated %d expired subscriptions", updated)
	}
	s.sendExpiryReminders(parent)
}

// sendExpiryReminders scans active grants only when dependencies, enable
// setting, transport readiness and scan lease permit. Nil repository/notifier
// is a no-op, missing settings default enabled, and unsuccessful lease
// acquisition skips this cycle. Successful acquisition supplies deferred
// release ownership.
//
// Settings/readiness share one budget; lease and every page get new contexts
// from parent. Each reminder send gets its own further context, so timing out
// one send cannot poison the next page's request. Pages request two hundred
// active rows ordered expiry ascending, stopping on missing pagination, final
// page or empty rows.
//
// Parent cancellation is checked before each item; page errors are observed
// and end the scan. The lease is not renewed here, and notification
// deduplication belongs to the notifier, so neither the fixed lease nor
// repeated cycles establishes exactly-once delivery.
func (s *SubscriptionExpiryService) sendExpiryReminders(parent context.Context) {
	if s == nil || s.userSubRepo == nil || s.notificationEmailService == nil {
		return
	}
	// Reuse one short budget for switch/readiness checks, then release it before
	// acquisition and page work; send-specific expiry cannot cancel these later
	// contexts.
	settingCtx, settingCancel := context.WithTimeout(parent, s.expiryReminderListTimeout())
	enabled := s.expiryReminderEnabled(settingCtx)
	if !enabled {
		settingCancel()
		return
	}
	if !s.smtpConfigured(settingCtx) {
		settingCancel()
		return
	}
	settingCancel()
	// Acquire scan ownership using an independent context and cancel it after
	// acquisition; an accepted lease's release remains usable through the
	// complete paginated scan.
	lockCtx, lockCancel := context.WithTimeout(parent, s.expiryReminderListTimeout())
	release, ok := s.lease(lockCtx, subscriptionExpiryReminderLeaderLockKey, s.instanceID, subscriptionExpiryReminderLeaderLockTTL)
	lockCancel()
	if !ok {
		return
	}
	// Release the accepted scan lease on every return, including page failure or
	// cancellation. No renewal loop extends its fixed lifetime during a longer
	// scan.
	defer release()

	for page := 1; ; page++ {
		// Create each page context from the still-running parent, so a prior
		// reminder's timeout does not mark the next list call canceled.
		ctx, cancel := context.WithTimeout(parent, s.expiryReminderListTimeout())
		// Read two hundred active rows per page in expiry order; missing pagination
		// or an empty result ends this bounded-page scan rather than requesting more
		// rows.
		subs, pag, err := s.userSubRepo.List(ctx, pagination.PaginationParams{Page: page, PageSize: 200}, nil, nil, SubscriptionStatusActive, "", "expires_at", "asc")
		cancel()
		// A failed page ends remaining reminder work while deferred lease release
		// still runs. Status maintenance has already returned; later cycles can
		// begin new pagination.
		if err != nil {
			s.observe("[SubscriptionExpiry] List active subscriptions for reminder failed: %v", err)
			return
		}
		for i := range subs {
			if parent.Err() != nil {
				return
			}
			s.sendExpiryReminderIfDue(parent, &subs[i])
		}
		if pag == nil || page >= pag.Pages || len(subs) == 0 {
			return
		}
	}
}

// expiryReminderEnabled reads the enable setting with default true for absent
// service/repository or missing key. Trimmed false, zero, off and disabled
// text turn reminders off; other text enables them, while nonmissing read
// errors are observed and return false.
func (s *SubscriptionExpiryService) expiryReminderEnabled(ctx context.Context) bool {
	// Standalone construction can omit the settings dependency; default enabled
	// keeps reminder selection usable while the configured application can supply
	// the persisted switch.
	if s == nil || s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, "subscription_expiry_notify_enabled")
	if err != nil {
		// An absent switch inherits enabled operation, but another read failure
		// leaves eligibility unknown and skips this cycle instead of treating
		// failure as configured permission.
		if errors.Is(err, settings.ErrSettingNotFound) {
			return true
		}
		s.observe("[SubscriptionExpiry] Read expiry reminder switch failed: %v", err)
		return false
	}
	return !isFalseReminderSetting(value)
}

// smtpConfigured checks a configured notifier's readiness and returns false
// for missing dependencies or a readiness error. Unconfigured transport uses
// the warning mutex and injected clock; other readiness errors are observed on
// each call.
//
// A zero last timestamp permits logging, and a nonzero one requires the
// warning interval to elapse before another observation. Logging stores the
// current clock value, so repeated injected zero readings keep the
// first-warning condition true. Callbacks execute under the warning mutex and
// avoid reentry. Readiness failure skips reminder work while leaving the
// separate status update intact.
func (s *SubscriptionExpiryService) smtpConfigured(ctx context.Context) bool {
	if s == nil || s.notificationEmailService == nil {
		return false
	}
	err := s.notificationEmailService.Ready(ctx)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrReminderTransportUnconfigured) {
		// Serialize warning timestamp selection and observation so repeated
		// unconfigured checks share one throttle; callbacks invoked inside this lock
		// must cooperate without reentry.
		s.smtpWarningMu.Lock()
		defer s.smtpWarningMu.Unlock()
		now := s.now()
		if s.lastSMTPWarning.IsZero() || now.Sub(s.lastSMTPWarning) >= subscriptionExpiryReminderSMTPWarningInterval {
			s.observe("[SubscriptionExpiry] SMTP is not configured; skipping expiry reminders")
			s.lastSMTPWarning = now
		}
		return false
	}
	// Other readiness failures are observed per call and leave the warning
	// timestamp unchanged. Returning false skips lease acquisition and every
	// reminder page for this cycle.
	s.observe("[SubscriptionExpiry] Read SMTP configuration failed; skipping expiry reminders: %v", err)
	return false
}

// sendExpiryReminderIfDue submits a reminder only for a loaded user with
// nonempty email and upward-rounded remaining days equal to seven, three or
// one. The supplied repository page determines active status; this helper does
// not repeat status admission.
//
// The injected clock chooses days, the loaded plan supplies
// display/localization data, and missing plan text uses the generic label.
// Each send uses its own parent-derived budget and observes its error without
// stopping later selected rows. Recipient language, notification preferences
// and successful-delivery identity are handled by the notifier.
func (s *SubscriptionExpiryService) sendExpiryReminderIfDue(parent context.Context, sub *UserSubscription) {
	if sub == nil || sub.User == nil || sub.User.Email == "" {
		return
	}
	daysRemaining := sub.DaysRemainingAt(s.now())
	// Use rounded lifetime days for the seven/three/one reminder keys; the
	// notifier owns replay suppression across repeated scans at the same
	// threshold.
	if daysRemaining != 7 && daysRemaining != 3 && daysRemaining != 1 {
		return
	}

	var localization PlanLocalization
	if sub.Plan != nil {
		localization = sub.Plan.Localization
	}
	// A fresh send context inherits worker cancellation while isolating this
	// reminder's deadline from later recipients and subsequent page reads in the
	// same scan.
	ctx, cancel := context.WithTimeout(parent, s.expiryReminderSendTimeout())
	defer cancel()
	// Observe a failed delivery without aborting later due rows or reversing
	// status maintenance; notifier identity and retry handling remain outside
	// this selector.
	if err := s.notificationEmailService.Send(ctx, ExpiryReminder{
		UserID: sub.UserID, SubscriptionID: sub.ID,
		RecipientEmail: sub.User.Email, RecipientName: reminderRecipientName(sub.User),
		PlanLocalization: localization, PlanName: subscriptionReminderPlanName(sub), ExpiresAt: sub.ExpiresAt, DaysRemaining: daysRemaining,
	}); err != nil {
		s.observe("[SubscriptionExpiry] Send expiry reminder failed: subscription=%d user=%d err=%v", sub.ID, sub.UserID, err)
	}
}

// expiredStatusUpdateTimeout returns a positive stored status-update budget or
// the fixed ten-second default. A nil service uses the same default; it
// selects a duration without starting or canceling work.
func (s *SubscriptionExpiryService) expiredStatusUpdateTimeout() time.Duration {
	if s != nil && s.updateTimeout > 0 {
		return s.updateTimeout
	}
	return subscriptionExpiryUpdateTimeout
}

// expiryReminderListTimeout returns a positive stored settings/lease/page
// budget or the fixed ten-second default. Nil service uses that default too;
// each operation constructs its own context from this selected duration.
func (s *SubscriptionExpiryService) expiryReminderListTimeout() time.Duration {
	if s != nil && s.reminderListTimeout > 0 {
		return s.reminderListTimeout
	}
	return subscriptionExpiryReminderListTimeout
}

// expiryReminderSendTimeout returns a positive stored per-reminder budget or
// the fixed thirty-second default. Nil service selects the default; changing
// the budget affects later sends rather than an already created context.
func (s *SubscriptionExpiryService) expiryReminderSendTimeout() time.Duration {
	if s != nil && s.reminderSendTimeout > 0 {
		return s.reminderSendTimeout
	}
	return subscriptionExpiryReminderSendTimeout
}

// subscriptionReminderPlanName returns loaded nonempty plan text or the
// generic Subscription label. Nil grant/plan also selects that label; the
// notifier may subsequently replace it using recipient-language plan
// localization.
func subscriptionReminderPlanName(sub *UserSubscription) string {
	if sub == nil || sub.Plan == nil || sub.Plan.Name == "" {
		return "Subscription"
	}
	return sub.Plan.Name
}

// isFalseReminderSetting reports trimmed case-folded false, zero, off or
// disabled values. Empty and unrecognized setting text return false, leaving
// reminders enabled at the caller's setting interpretation.
func isFalseReminderSetting(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "off", "disabled":
		return true
	default:
		return false
	}
}

// reminderRecipientName selects a nonblank trimmed username, then trimmed
// email from a nonnil user. The due-reminder caller establishes user presence
// and nonempty raw email; this helper supplies display text for notification
// formatting.
func reminderRecipientName(user *UserSummary) string {
	if name := strings.TrimSpace(user.Username); name != "" {
		return name
	}
	return strings.TrimSpace(user.Email)
}
