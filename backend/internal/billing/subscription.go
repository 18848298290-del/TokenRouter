package billing

import (
	"math"
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

const (
	// Subscription status constants.
	SubscriptionStatusActive    = "active"
	SubscriptionStatusPending   = "pending"
	SubscriptionStatusExpired   = "expired"
	SubscriptionStatusSuspended = "suspended"

	SubscriptionStatusRevoked = "revoked"

	SubscriptionDailyWindow   = 24 * time.Hour
	SubscriptionWeeklyWindow  = 7 * 24 * time.Hour
	SubscriptionMonthlyWindow = 30 * 24 * time.Hour
)

type APIKeyBillingContext struct {
	Mode, Source string
	Subscription *UserSubscription
	Available    bool
}

type UserSubscription struct {
	ID     int64
	UserID int64
	PlanID int64

	StartsAt  time.Time
	ExpiresAt time.Time
	Status    string

	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time

	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64

	DailyUsageUSD   float64
	WeeklyUsageUSD  float64
	MonthlyUsageUSD float64

	AssignedBy    *int64
	AssignedAt    time.Time
	SourceOrderID *int64
	Notes         string

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	User           *UserSummary
	Plan           *SubscriptionPlan
	AssignedByUser *UserSummary
}

// SubscriptionWindowActivation identifies quota windows eligible for first activation.
type SubscriptionWindowActivation struct {
	Daily   bool
	Weekly  bool
	Monthly bool
}

// SubscriptionRemainingForDisplay returns available quota, or -1 when every window is unlimited.
func SubscriptionRemainingForDisplay(sub *UserSubscription) float64 {
	if sub == nil {
		return 0
	}
	if (sub.DailyLimitUSD == nil || *sub.DailyLimitUSD <= 0) && (sub.WeeklyLimitUSD == nil || *sub.WeeklyLimitUSD <= 0) && (sub.MonthlyLimitUSD == nil || *sub.MonthlyLimitUSD <= 0) {
		return -1
	}
	return sub.AvailableQuotaUSD()
}

func (a SubscriptionWindowActivation) Any() bool {
	return a.Daily || a.Weekly || a.Monthly
}

func (s *UserSubscription) IsActive() bool {
	now := time.Now()
	return s.DeletedAt == nil && s.Status == SubscriptionStatusActive && !now.Before(s.StartsAt) && now.Before(s.ExpiresAt)
}

func (s *UserSubscription) IsPending() bool {
	if s == nil {
		return false
	}
	return s.DeletedAt == nil && s.Status == SubscriptionStatusPending && time.Now().Before(s.StartsAt)
}

func (s *UserSubscription) IsEffective() bool {
	if s == nil {
		return false
	}
	now := time.Now()
	return !now.Before(s.StartsAt) && now.Before(s.ExpiresAt) && s.EffectiveStatus(now) == SubscriptionStatusActive
}

func (s *UserSubscription) EffectiveStatus(now time.Time) string {
	if s == nil {
		return SubscriptionStatusExpired
	}
	if s.DeletedAt != nil {
		return SubscriptionStatusRevoked
	}
	if !s.ExpiresAt.After(now) {
		return SubscriptionStatusExpired
	}
	if now.Before(s.StartsAt) {
		return SubscriptionStatusPending
	}
	switch s.Status {
	case SubscriptionStatusSuspended:
		return SubscriptionStatusSuspended
	default:
		return SubscriptionStatusActive
	}
}

func (s *UserSubscription) IsExpired() bool {
	return !s.ExpiresAt.After(time.Now())
}

func (s *UserSubscription) DaysRemaining() int {
	return s.DaysRemainingAt(time.Now())
}

func (s *UserSubscription) DaysRemainingAt(now time.Time) int {
	remaining := s.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}

	days := int(remaining / SubscriptionDailyWindow)
	if remaining%SubscriptionDailyWindow != 0 {
		days++
	}
	return days
}

func (s *UserSubscription) HasQuotaLimit() bool {
	return PositiveSubscriptionLimit(s.DailyLimitUSD) ||
		PositiveSubscriptionLimit(s.WeeklyLimitUSD) ||
		PositiveSubscriptionLimit(s.MonthlyLimitUSD)
}

func (s *UserSubscription) HasOneTimeDailyQuota() bool {
	if s == nil || s.StartsAt.IsZero() || s.ExpiresAt.IsZero() {
		return false
	}
	return !s.ExpiresAt.After(s.StartsAt.AddDate(0, 0, 1))
}

func (s *UserSubscription) NeedsWindowActivationAt(now time.Time) bool {
	return s.WindowActivationAt(now).Any()
}

// WindowActivationAt reports windows that can start; a positive outer limit permits shorter windows near expiry.
func (s *UserSubscription) WindowActivationAt(now time.Time) SubscriptionWindowActivation {
	var activation SubscriptionWindowActivation
	if s == nil || !s.HasQuotaLimit() || now.Before(s.StartsAt) || !now.Before(s.ExpiresAt) {
		return activation
	}

	windowStart := startOfDay(now)
	if PositiveSubscriptionLimit(s.DailyLimitUSD) && s.DailyWindowStart == nil {
		activation.Daily = s.HasOneTimeDailyQuota() ||
			s.CanStartQuotaWindow(windowStart, SubscriptionDailyWindow)
	}
	if PositiveSubscriptionLimit(s.WeeklyLimitUSD) && s.WeeklyWindowStart == nil {
		activation.Weekly = s.CanStartQuotaWindow(windowStart, SubscriptionWeeklyWindow)
	}
	if PositiveSubscriptionLimit(s.MonthlyLimitUSD) && s.MonthlyWindowStart == nil {
		activation.Monthly = s.CanStartQuotaWindow(windowStart, SubscriptionMonthlyWindow)
	}
	return activation
}

func (s *UserSubscription) NeedsDailyReset(calendar timezone.Calendar) bool {
	return s.NeedsDailyResetAt(time.Now(), calendar)
}

func (s *UserSubscription) NeedsDailyResetAt(now time.Time, calendar timezone.Calendar) bool {
	_, ok := s.AutomaticDailyWindowStartWithCalendar(now, calendar)
	return ok
}

// AutomaticDailyWindowStartWithCalendar selects a new calendar-day start subject to one-time and expiry-tail rules.
func (s *UserSubscription) AutomaticDailyWindowStartWithCalendar(now time.Time, calendar timezone.Calendar) (time.Time, bool) {
	if s == nil || s.DailyWindowStart == nil || s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) {
		return time.Time{}, false
	}
	if s.HasOneTimeDailyQuota() {
		return time.Time{}, false
	}
	today := calendar.StartOfDay(now)
	if !today.After(calendar.StartOfDay(*s.DailyWindowStart)) {
		return time.Time{}, false
	}
	if !s.CanStartQuotaWindow(today, SubscriptionDailyWindow) {
		return time.Time{}, false
	}
	return today, true
}

func (s *UserSubscription) NeedsWeeklyReset() bool {
	return s.NeedsWeeklyResetAt(time.Now())
}

func (s *UserSubscription) NeedsWeeklyResetAt(now time.Time) bool {
	if s == nil || s.WeeklyWindowStart == nil || s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) {
		return false
	}
	if now.Before(s.WeeklyWindowStart.Add(SubscriptionWeeklyWindow)) {
		return false
	}
	return s.CanStartQuotaWindow(startOfDay(now), SubscriptionWeeklyWindow)
}

func (s *UserSubscription) NeedsMonthlyReset() bool {
	return s.NeedsMonthlyResetAt(time.Now())
}

func (s *UserSubscription) NeedsMonthlyResetAt(now time.Time) bool {
	if s == nil || s.MonthlyWindowStart == nil || s.ExpiresAt.IsZero() || !now.Before(s.ExpiresAt) {
		return false
	}
	if now.Before(s.MonthlyWindowStart.Add(SubscriptionMonthlyWindow)) {
		return false
	}
	return s.CanStartQuotaWindow(startOfDay(now), SubscriptionMonthlyWindow)
}

// CanStartFullQuotaWindow reports whether a complete window fits before subscription expiry.
func (s *UserSubscription) CanStartFullQuotaWindow(windowStart time.Time, duration time.Duration) bool {
	if s == nil || s.ExpiresAt.IsZero() || duration <= 0 || !windowStart.Before(s.ExpiresAt) {
		return false
	}
	return !windowStart.Add(duration).After(s.ExpiresAt)
}

// CanStartQuotaWindow permits a full window or a shorter expiry-tail window protected by a positive outer limit.
// @project-doc docs/domains/payments_and_entitlements.md#subscription_quota_windows
func (s *UserSubscription) CanStartQuotaWindow(windowStart time.Time, duration time.Duration) bool {
	if s == nil || s.ExpiresAt.IsZero() || !windowStart.Before(s.ExpiresAt) {
		return false
	}
	return s.CanStartFullQuotaWindow(windowStart, duration) || s.HasFiniteOuterQuotaLimit(duration)
}

// HasFiniteOuterQuotaLimit checks positive outer limits in daily, weekly, monthly order; absent or nonpositive limits provide no protection.
func (s *UserSubscription) HasFiniteOuterQuotaLimit(duration time.Duration) bool {
	if s == nil {
		return false
	}
	switch duration {
	case SubscriptionDailyWindow:
		return PositiveSubscriptionLimit(s.WeeklyLimitUSD) || PositiveSubscriptionLimit(s.MonthlyLimitUSD)
	case SubscriptionWeeklyWindow:
		return PositiveSubscriptionLimit(s.MonthlyLimitUSD)
	default:
		return false
	}
}

func (s *UserSubscription) DailyResetTime(calendar timezone.Calendar) *time.Time {
	if s.DailyWindowStart == nil {
		return nil
	}
	if s.HasOneTimeDailyQuota() {
		t := s.ExpiresAt
		return &t
	}
	// Daily resets align with midnight, including windows whose stored start was not at midnight.
	t := calendar.StartOfDay(*s.DailyWindowStart).AddDate(0, 0, 1)
	if !s.CanStartQuotaWindow(t, SubscriptionDailyWindow) {
		t = s.ExpiresAt
	}
	return &t
}

func (s *UserSubscription) WeeklyResetTime() *time.Time {
	if s.WeeklyWindowStart == nil {
		return nil
	}
	t := s.WeeklyWindowStart.Add(SubscriptionWeeklyWindow)
	if !s.CanStartQuotaWindow(t, SubscriptionWeeklyWindow) {
		t = s.ExpiresAt
	}
	return &t
}

func (s *UserSubscription) MonthlyResetTime() *time.Time {
	if s.MonthlyWindowStart == nil {
		return nil
	}
	t := s.MonthlyWindowStart.Add(SubscriptionMonthlyWindow)
	if !s.CanStartQuotaWindow(t, SubscriptionMonthlyWindow) {
		t = s.ExpiresAt
	}
	return &t
}

func (s *UserSubscription) RemainingDailyUSD() *float64 {
	return RemainingWindowAmount(s.DailyLimitUSD, s.DailyUsageUSD)
}

func (s *UserSubscription) RemainingWeeklyUSD() *float64 {
	return RemainingWindowAmount(s.WeeklyLimitUSD, s.WeeklyUsageUSD)
}

func (s *UserSubscription) RemainingMonthlyUSD() *float64 {
	return RemainingWindowAmount(s.MonthlyLimitUSD, s.MonthlyUsageUSD)
}

func (s *UserSubscription) AvailableQuotaUSD() float64 {
	return MinRemainingWindowAmount(
		s.RemainingDailyUSD(),
		s.RemainingWeeklyUSD(),
		s.RemainingMonthlyUSD(),
	)
}

// HighestQuotaExhausted reports whether the highest configured finite quota has been consumed.
// Monthly takes precedence over weekly, then daily; a remaining outer quota blocks self-revocation.
// @project-doc docs/domains/payments_and_entitlements.md#subscription_self_revoke
func (s *UserSubscription) HighestQuotaExhausted() bool {
	if s == nil {
		return false
	}
	for _, quota := range []struct {
		limit float64
		used  float64
		ok    bool
	}{
		{limit: ValueOrZeroLimit(s.MonthlyLimitUSD), used: s.MonthlyUsageUSD, ok: FinitePositiveSubscriptionLimit(s.MonthlyLimitUSD)},
		{limit: ValueOrZeroLimit(s.WeeklyLimitUSD), used: s.WeeklyUsageUSD, ok: FinitePositiveSubscriptionLimit(s.WeeklyLimitUSD)},
		{limit: ValueOrZeroLimit(s.DailyLimitUSD), used: s.DailyUsageUSD, ok: FinitePositiveSubscriptionLimit(s.DailyLimitUSD)},
	} {
		if quota.ok {
			return quota.used >= quota.limit
		}
	}
	return false
}

func ValueOrZeroLimit(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func FinitePositiveSubscriptionLimit(limit *float64) bool {
	return PositiveSubscriptionLimit(limit) && !math.IsNaN(*limit) && !math.IsInf(*limit, 0)
}

func RemainingWindowAmount(limit *float64, used float64) *float64 {
	if limit == nil || *limit <= 0 {
		return nil
	}
	remaining := *limit - used
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

func MinRemainingWindowAmount(values ...*float64) float64 {
	var (
		min   float64
		found bool
	)
	for _, value := range values {
		if value == nil {
			continue
		}
		if !found || *value < min {
			min = *value
			found = true
		}
	}
	if !found {
		return 0
	}
	return min
}

func PositiveSubscriptionLimit(limit *float64) bool {
	return limit != nil && *limit > 0
}

// startOfDay uses t's own timezone; callers must supply the timezone belonging to their window.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func subscriptionPlanIncludesGroup(plan *SubscriptionPlan, groupID int64) bool {
	if plan == nil || groupID <= 0 {
		return false
	}
	if len(plan.GroupIDs) == 0 {
		return true
	}
	return slices.Contains(plan.GroupIDs, groupID)
}

// SubscriptionAllowsGroup reports whether the subscription's plan covers the group.
// An empty plan group list is unrestricted; missing plans and nonpositive group IDs are rejected.
func SubscriptionAllowsGroup(subscription *UserSubscription, groupID int64) bool {
	if subscription == nil || subscription.Plan == nil || groupID <= 0 {
		return false
	}
	return subscriptionPlanIncludesGroup(subscription.Plan, groupID)
}
