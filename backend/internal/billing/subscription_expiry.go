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

const (
	subscriptionExpiryUpdateTimeout               = 10 * time.Second
	subscriptionExpiryReminderListTimeout         = 10 * time.Second
	subscriptionExpiryReminderSendTimeout         = 30 * time.Second
	subscriptionExpiryReminderSMTPWarningInterval = time.Minute
	subscriptionExpiryReminderLeaderLockKey       = "subscription:expiry:reminder:leader"
	// The lease covers a paginated reminder scan, which can outlast an individual database operation.
	subscriptionExpiryReminderLeaderLockTTL = 5 * time.Minute
)

var ErrReminderTransportUnconfigured = errors.New("subscription reminder transport is not configured")

// ExpiryReminder contains a reminder already selected by eligibility and timing rules for delivery.
type ExpiryReminder struct {
	UserID, SubscriptionID                  int64
	RecipientEmail, RecipientName, PlanName string
	ExpiresAt                               time.Time
	DaysRemaining                           int
	PlanLocalization                        PlanLocalization
}

// ExpiryNotifier reports delivery readiness and sends subscription reminders.
type ExpiryNotifier interface {
	Ready(context.Context) error
	Send(context.Context, ExpiryReminder) error
}

type ExpirySettings interface {
	GetValue(context.Context, string) (string, error)
}
type ExpiryLease func(context.Context, string, string, time.Duration) (func(), bool)

// ExpiryOptions configures the expiry scan's interval, clock, and lease; Start launches the worker.
type ExpiryOptions struct {
	Interval time.Duration
	Owner    string
	Now      func() time.Time
	Observe  func(string, ...any)
	Settings ExpirySettings
	Notifier ExpiryNotifier
	Lease    ExpiryLease
}

// SubscriptionExpiryService owns expiry scanning, reminder selection, and worker lifecycle.
type SubscriptionExpiryService struct {
	userSubRepo                                                       UserSubscriptionRepository
	settingRepo                                                       ExpirySettings
	notificationEmailService                                          ExpiryNotifier
	interval, updateTimeout, reminderListTimeout, reminderSendTimeout time.Duration
	instanceID                                                        string
	now                                                               func() time.Time
	observe                                                           func(string, ...any)
	lease                                                             ExpiryLease
	mu                                                                sync.Mutex
	started, stopped                                                  bool
	cancel                                                            context.CancelFunc
	done                                                              chan struct{}
	smtpWarningMu                                                     sync.Mutex
	lastSMTPWarning                                                   time.Time
}

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

// Start launches at most one worker and cannot restart a stopped service.
func (s *SubscriptionExpiryService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped || s.userSubRepo == nil || s.interval <= 0 {
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
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

// StopContext cancels scanning and waits for in-flight work until the caller's context expires.
func (s *SubscriptionExpiryService) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
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
func (s *SubscriptionExpiryService) Stop() { _ = s.StopContext(context.Background()) }

func (s *SubscriptionExpiryService) runOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, s.expiredStatusUpdateTimeout())
	updated, err := s.userSubRepo.BatchUpdateExpiredStatus(ctx)
	cancel()
	if err != nil {
		s.observe("[SubscriptionExpiry] Update expired subscriptions failed: %v", err)
		return
	}
	if updated > 0 {
		s.observe("[SubscriptionExpiry] Updated %d expired subscriptions", updated)
	}
	s.sendExpiryReminders(parent)
}

func (s *SubscriptionExpiryService) sendExpiryReminders(parent context.Context) {
	if s == nil || s.userSubRepo == nil || s.notificationEmailService == nil {
		return
	}
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
	lockCtx, lockCancel := context.WithTimeout(parent, s.expiryReminderListTimeout())
	release, ok := s.lease(lockCtx, subscriptionExpiryReminderLeaderLockKey, s.instanceID, subscriptionExpiryReminderLeaderLockTTL)
	lockCancel()
	if !ok {
		return
	}
	defer release()

	for page := 1; ; page++ {
		ctx, cancel := context.WithTimeout(parent, s.expiryReminderListTimeout())
		subs, pag, err := s.userSubRepo.List(ctx, pagination.PaginationParams{Page: page, PageSize: 200}, nil, nil, SubscriptionStatusActive, "", "expires_at", "asc")
		cancel()
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

func (s *SubscriptionExpiryService) expiryReminderEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, "subscription_expiry_notify_enabled")
	if err != nil {
		if errors.Is(err, settings.ErrSettingNotFound) {
			return true
		}
		s.observe("[SubscriptionExpiry] Read expiry reminder switch failed: %v", err)
		return false
	}
	return !isFalseReminderSetting(value)
}

func (s *SubscriptionExpiryService) smtpConfigured(ctx context.Context) bool {
	if s == nil || s.notificationEmailService == nil {
		return false
	}
	err := s.notificationEmailService.Ready(ctx)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrReminderTransportUnconfigured) {
		s.smtpWarningMu.Lock()
		defer s.smtpWarningMu.Unlock()
		now := s.now()
		if s.lastSMTPWarning.IsZero() || now.Sub(s.lastSMTPWarning) >= subscriptionExpiryReminderSMTPWarningInterval {
			s.observe("[SubscriptionExpiry] SMTP is not configured; skipping expiry reminders")
			s.lastSMTPWarning = now
		}
		return false
	}
	s.observe("[SubscriptionExpiry] Read SMTP configuration failed; skipping expiry reminders: %v", err)
	return false
}

func (s *SubscriptionExpiryService) sendExpiryReminderIfDue(parent context.Context, sub *UserSubscription) {
	if sub == nil || sub.User == nil || sub.User.Email == "" {
		return
	}
	daysRemaining := sub.DaysRemainingAt(s.now())
	if daysRemaining != 7 && daysRemaining != 3 && daysRemaining != 1 {
		return
	}

	var localization PlanLocalization
	if sub.Plan != nil {
		localization = sub.Plan.Localization
	}
	ctx, cancel := context.WithTimeout(parent, s.expiryReminderSendTimeout())
	defer cancel()
	if err := s.notificationEmailService.Send(ctx, ExpiryReminder{
		UserID: sub.UserID, SubscriptionID: sub.ID,
		RecipientEmail: sub.User.Email, RecipientName: reminderRecipientName(sub.User),
		PlanLocalization: localization, PlanName: subscriptionReminderPlanName(sub), ExpiresAt: sub.ExpiresAt, DaysRemaining: daysRemaining,
	}); err != nil {
		s.observe("[SubscriptionExpiry] Send expiry reminder failed: subscription=%d user=%d err=%v", sub.ID, sub.UserID, err)
	}
}

func (s *SubscriptionExpiryService) expiredStatusUpdateTimeout() time.Duration {
	if s != nil && s.updateTimeout > 0 {
		return s.updateTimeout
	}
	return subscriptionExpiryUpdateTimeout
}

func (s *SubscriptionExpiryService) expiryReminderListTimeout() time.Duration {
	if s != nil && s.reminderListTimeout > 0 {
		return s.reminderListTimeout
	}
	return subscriptionExpiryReminderListTimeout
}

func (s *SubscriptionExpiryService) expiryReminderSendTimeout() time.Duration {
	if s != nil && s.reminderSendTimeout > 0 {
		return s.reminderSendTimeout
	}
	return subscriptionExpiryReminderSendTimeout
}

func subscriptionReminderPlanName(sub *UserSubscription) string {
	if sub == nil || sub.Plan == nil || sub.Plan.Name == "" {
		return "Subscription"
	}
	return sub.Plan.Name
}

// isFalseReminderSetting recognizes disabled values; an empty or unrecognized value leaves reminders enabled.
func isFalseReminderSetting(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "off", "disabled":
		return true
	default:
		return false
	}
}

func reminderRecipientName(user *UserSummary) string {
	if name := strings.TrimSpace(user.Username); name != "" {
		return name
	}
	return strings.TrimSpace(user.Email)
}
