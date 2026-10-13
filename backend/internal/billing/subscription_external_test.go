package billing_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// TestUserSubscriptionNeedsDailyReset_DailyCardKeepsOneTimeQuota gives a
// one-day lifetime with an established daily anchor and usage. Crossing the
// next midnight must keep reset eligibility false for that one-time allowance.
func TestUserSubscriptionNeedsDailyReset_DailyCardKeepsOneTimeQuota(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	sub := &billing.UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyUsageUSD:    10,
	}

	require.True(t, sub.HasOneTimeDailyQuota())
	require.False(t, sub.NeedsDailyResetAt(dailyWindowStart.Add(25*time.Hour), timezone.NewCalendar(time.UTC)), "日卡应作为一次性配额，跨 0 点后不再刷新日额度")
}

// TestUserSubscriptionNeedsDailyReset_MultiDaySubscriptionStillRefreshes gives
// a two-day UTC lifetime and stored daily anchor. It checks the lifetime is
// not one-time and a full next-day window can reset.
func TestUserSubscriptionNeedsDailyReset_MultiDaySubscriptionStillRefreshes(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	sub := &billing.UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.AddDate(0, 0, 2),
		DailyWindowStart: &dailyWindowStart,
	}

	require.False(t, sub.HasOneTimeDailyQuota())
	require.True(t, sub.NeedsDailyResetAt(dailyWindowStart.Add(24*time.Hour), timezone.NewCalendar(time.UTC)), "多日订阅仍应在次日零点刷新")
}

// TestUserSubscriptionNeedsDailyReset_LegacyAnchorHealsAtNextMidnight stores
// an afternoon anchor inside a longer UTC lifetime. Reset eligibility stays
// false that night and becomes true just after the next calendar midnight.
func TestUserSubscriptionNeedsDailyReset_LegacyAnchorHealsAtNextMidnight(t *testing.T) {
	base := timezone.NewCalendar(time.UTC).
		StartOfDay(time.Now().UTC())
	legacyWindowStart := base.Add(16*time.Hour + 49*time.Minute)
	sub := &billing.UserSubscription{
		StartsAt:         base.AddDate(0, 0, -3),
		ExpiresAt:        base.AddDate(0, 0, 10),
		DailyWindowStart: &legacyWindowStart,
	}

	require.False(t, sub.NeedsDailyResetAt(base.Add(23*time.Hour+59*time.Minute), timezone.NewCalendar(time.UTC)), "同一日历日内不应重置日额度")
	require.True(t, sub.NeedsDailyResetAt(base.AddDate(0, 0, 1).Add(time.Minute), timezone.NewCalendar(time.UTC)), "旧的非零点锚点应在下一个零点后自愈")
}

// TestUserSubscriptionNeedsDailyReset_SkipsExpiryTailWindow reaches a new
// daily boundary with less than eight hours before expiry and no outer limit.
// The helper must reject granting another full unprotected daily period.
func TestUserSubscriptionNeedsDailyReset_SkipsExpiryTailWindow(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	sub := &billing.UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
	}

	require.False(t, sub.NeedsDailyResetAt(now, timezone.NewCalendar(time.UTC)), "到期当天不足完整日窗口时不应额外刷新 daily usage")
}

// TestUserSubscriptionNeedsDailyReset_ExpiryTailUsesFiniteOuterLimit tests a
// short expiry tail with positive weekly/monthly protection, zero protection
// and no protection. Only the supplied positive outer cases permit daily
// reset.
func TestUserSubscriptionNeedsDailyReset_ExpiryTailUsesFiniteOuterLimit(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	dailyLimit := 10.0
	outerLimit := 100.0
	unlimited := 0.0

	tests := []struct {
		name string // named quota scenario label
		// weeklyLimit and monthlyLimit supply alternative outer protection for this
		// daily tail. Positive supplied limits permit a reset; zero and absent
		// pointers keep the candidate unprotected.
		weeklyLimit  *float64 // USD
		monthlyLimit *float64 // USD
		want         bool     // expected quota eligibility result
	}{
		{name: "有限周额度", weeklyLimit: &outerLimit, want: true},
		{name: "有限月额度", monthlyLimit: &outerLimit, want: true},
		{name: "零值周额度", weeklyLimit: &unlimited, want: false},
		{name: "没有外层额度", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &billing.UserSubscription{
				StartsAt:         start,
				ExpiresAt:        expiresAt,
				DailyWindowStart: &dailyWindowStart,
				DailyLimitUSD:    &dailyLimit,
				WeeklyLimitUSD:   tt.weeklyLimit,
				MonthlyLimitUSD:  tt.monthlyLimit,
			}

			require.Equal(t, tt.want, sub.NeedsDailyResetAt(now, timezone.NewCalendar(time.UTC)))
		})
	}
}

// TestUserSubscriptionNeedsWeeklyReset_ExpiryTailUsesFiniteMonthlyLimit
// reaches a seven-day anchor boundary with less than eight hours remaining.
// Weekly reset succeeds under a positive monthly limit and fails when that
// pointer is removed.
func TestUserSubscriptionNeedsWeeklyReset_ExpiryTailUsesFiniteMonthlyLimit(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	weeklyWindowStart := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	weeklyLimit := 50.0
	monthlyLimit := 100.0
	sub := &billing.UserSubscription{
		StartsAt:          start,
		ExpiresAt:         expiresAt,
		WeeklyWindowStart: &weeklyWindowStart,
		WeeklyLimitUSD:    &weeklyLimit,
		MonthlyLimitUSD:   &monthlyLimit,
	}

	require.True(t, sub.NeedsWeeklyResetAt(now), "有限月额度应允许周额度在订阅尾段刷新")
	sub.MonthlyLimitUSD = nil
	require.False(t, sub.NeedsWeeklyResetAt(now), "周额度没有外层保护时仍须容纳完整周窗口")
}

// TestUserSubscriptionNeedsDailyReset_DailyCardIgnoresFiniteOuterLimit crosses
// midnight during a one-day grant with daily and monthly limits. The
// established daily quota must remain one-time despite positive outer
// protection.
func TestUserSubscriptionNeedsDailyReset_DailyCardIgnoresFiniteOuterLimit(t *testing.T) {
	start := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	dailyLimit := 10.0
	monthlyLimit := 100.0
	sub := &billing.UserSubscription{
		StartsAt:         start,
		ExpiresAt:        start.Add(24 * time.Hour),
		DailyWindowStart: &dailyWindowStart,
		DailyLimitUSD:    &dailyLimit,
		MonthlyLimitUSD:  &monthlyLimit,
	}

	require.False(t, sub.NeedsDailyResetAt(time.Date(2026, 5, 31, 0, 5, 0, 0, time.UTC), timezone.NewCalendar(time.UTC)), "1 日卡始终只有一份日额度")
}

// TestUserSubscriptionNeedsMonthlyReset_SkipsExpiryTailWindow reaches thirty
// elapsed days since its anchor shortly before grant expiry. It checks the
// remaining time cannot admit another full monthly period.
func TestUserSubscriptionNeedsMonthlyReset_SkipsExpiryTailWindow(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	monthlyWindowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	sub := &billing.UserSubscription{
		StartsAt:           start,
		ExpiresAt:          expiresAt,
		MonthlyWindowStart: &monthlyWindowStart,
	}

	require.False(t, sub.NeedsMonthlyResetAt(now), "到期当天不足完整月窗口时不应额外刷新 monthly usage")
}

// TestUserSubscriptionDailyResetTime_DailyCardReturnsExpiry asks for display
// timing on a one-day lifetime with a stored daily anchor. The returned nonnil
// quota end must equal subscription expiry.
func TestUserSubscriptionDailyResetTime_DailyCardReturnsExpiry(t *testing.T) {
	start := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	expiresAt := start.Add(24 * time.Hour)
	sub := &billing.UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
	}

	resetAt := sub.DailyResetTime(timezone.NewCalendar(time.UTC))
	require.NotNil(t, resetAt)
	require.Equal(t, expiresAt, *resetAt, "日卡展示的日额度结束时间应为订阅过期时间")
}

// TestUserSubscriptionDailyResetTime_LegacyAnchorReturnsNextMidnight uses an
// afternoon stored anchor in a longer UTC lifetime. It checks the displayed
// daily reset is the next calendar midnight, not twenty-four hours after that
// anchor.
func TestUserSubscriptionDailyResetTime_LegacyAnchorReturnsNextMidnight(t *testing.T) {
	base := timezone.NewCalendar(time.UTC).
		StartOfDay(time.Now().UTC())
	legacyWindowStart := base.Add(16*time.Hour + 49*time.Minute)
	sub := &billing.UserSubscription{
		StartsAt:         base.AddDate(0, 0, -3),
		ExpiresAt:        base.AddDate(0, 0, 10),
		DailyWindowStart: &legacyWindowStart,
	}

	resetAt := sub.DailyResetTime(timezone.NewCalendar(time.UTC))
	require.NotNil(t, resetAt)
	require.Equal(t, base.AddDate(0, 0, 1), *resetAt, "旧的非零点锚点应展示其所在日的下一个零点")
}

// TestUserSubscriptionMonthlyResetTime_TailWindowReturnsExpiry reaches the
// final monthly period with only a short lifetime tail after its candidate.
// The nonnil display deadline must become subscription expiry.
func TestUserSubscriptionMonthlyResetTime_TailWindowReturnsExpiry(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	monthlyWindowStart := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	sub := &billing.UserSubscription{
		StartsAt:           start,
		ExpiresAt:          expiresAt,
		MonthlyWindowStart: &monthlyWindowStart,
	}

	resetAt := sub.MonthlyResetTime()
	require.NotNil(t, resetAt)
	require.Equal(t, expiresAt, *resetAt, "到期尾段展示的月额度结束时间应为订阅过期时间")
}

// TestUserSubscriptionDailyResetTime_TailWindowUsesFiniteMonthlyLimit supplies
// positive monthly protection near the final daily boundary. It checks the
// daily display keeps its ordinary next reset instant despite the shorter
// lifetime tail.
func TestUserSubscriptionDailyResetTime_TailWindowUsesFiniteMonthlyLimit(t *testing.T) {
	start := time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC)
	dailyWindowStart := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	monthlyLimit := 100.0
	sub := &billing.UserSubscription{
		StartsAt:         start,
		ExpiresAt:        expiresAt,
		DailyWindowStart: &dailyWindowStart,
		MonthlyLimitUSD:  &monthlyLimit,
	}

	resetAt := sub.DailyResetTime(timezone.NewCalendar(time.UTC))
	require.NotNil(t, resetAt)
	require.Equal(t, dailyWindowStart.Add(billing.SubscriptionDailyWindow), *resetAt, "有限月额度保护下应展示尾段日窗口的实际刷新时间")
}

// TestUserSubscriptionWindowActivation_MixedDailyMonthlyTailActivatesOnlyDaily
// supplies missing daily/monthly anchors and a two-hour lifetime tail.
// Positive monthly protection permits daily activation while weekly and
// monthly selections stay false.
func TestUserSubscriptionWindowActivation_MixedDailyMonthlyTailActivatesOnlyDaily(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	dailyLimit := 10.0
	monthlyLimit := 100.0
	sub := &billing.UserSubscription{
		ID:              1,
		Status:          billing.SubscriptionStatusActive,
		StartsAt:        now.AddDate(0, 0, -29),
		ExpiresAt:       now.Add(2 * time.Hour),
		DailyLimitUSD:   &dailyLimit,
		MonthlyLimitUSD: &monthlyLimit,
		MonthlyUsageUSD: 90,
	}

	activation := sub.WindowActivationAt(now)

	require.True(t, activation.Daily, "有限月额度存在时，到期尾段应激活日额度窗口")
	require.False(t, activation.Weekly)
	require.False(t, activation.Monthly, "不足完整月窗口时不应顺带激活月额度窗口")
}

// TestUserSubscriptionWindowActivation_MixedWeeklyMonthlyTailActivatesOnlyWeekly
// supplies missing weekly/monthly anchors and a two-hour lifetime tail. The
// positive monthly limit protects weekly activation, while daily and monthly
// selections remain false.
func TestUserSubscriptionWindowActivation_MixedWeeklyMonthlyTailActivatesOnlyWeekly(t *testing.T) {
	now := time.Date(2026, 5, 30, 0, 5, 0, 0, time.UTC)
	weeklyLimit := 50.0
	monthlyLimit := 100.0
	sub := &billing.UserSubscription{
		ID:              1,
		Status:          billing.SubscriptionStatusActive,
		StartsAt:        now.AddDate(0, 0, -29),
		ExpiresAt:       now.Add(2 * time.Hour),
		WeeklyLimitUSD:  &weeklyLimit,
		MonthlyLimitUSD: &monthlyLimit,
		MonthlyUsageUSD: 90,
	}

	activation := sub.WindowActivationAt(now)

	require.False(t, activation.Daily)
	require.True(t, activation.Weekly, "有限月额度存在时，到期尾段应激活周额度窗口")
	require.False(t, activation.Monthly, "月额度没有更高层保护，不应在尾段激活")
}

// TestUserSubscriptionHighestQuotaExhaustedUsesHighestConfiguredWindow
// combines outer/lower quota exhaustion, absent limits and positive infinity.
// It checks monthly-then-weekly-then-daily precedence, with available outer
// quotas, all-absent limits and the supplied infinite limit denying the
// predicate.
func TestUserSubscriptionHighestQuotaExhaustedUsesHighestConfiguredWindow(t *testing.T) {
	tests := []struct {
		name string // named quota scenario label
		// monthly, weekly, daily and monthlyUse, weeklyUse, dailyUse are copied into
		// each grant's limit/counter pairs. Nil removes a configured period, and the
		// infinity case cannot authorize self-revocation.
		monthly    *float64 // USD
		monthlyUse float64  // USD
		weekly     *float64 // USD
		weeklyUse  float64  // USD
		daily      *float64 // USD
		dailyUse   float64  // USD
		want       bool     // expected quota eligibility result
	}{
		{name: "monthly exhausted", monthly: quotaPointer(100), monthlyUse: 100, weekly: quotaPointer(10), weeklyUse: 10, daily: quotaPointer(1), dailyUse: 1, want: true},
		{name: "monthly available blocks lower exhausted windows", monthly: quotaPointer(100), monthlyUse: 99, weekly: quotaPointer(10), weeklyUse: 10, daily: quotaPointer(1), dailyUse: 1, want: false},
		{name: "weekly exhausted when monthly absent", weekly: quotaPointer(10), weeklyUse: 10, daily: quotaPointer(1), dailyUse: 1, want: true},
		{name: "weekly available blocks daily exhausted", weekly: quotaPointer(10), weeklyUse: 9, daily: quotaPointer(1), dailyUse: 1, want: false},
		{name: "daily exhausted when it is the only limit", daily: quotaPointer(1), dailyUse: 1, want: true},
		{name: "unlimited has no revocable quota", daily: nil, want: false},
		{name: "non-finite quota is not revocable", monthly: quotaPointer(math.Inf(1)), monthlyUse: math.Inf(1), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := revokeSubscriptionFixture()
			sub.MonthlyLimitUSD, sub.MonthlyUsageUSD = tt.monthly, tt.monthlyUse
			sub.WeeklyLimitUSD, sub.WeeklyUsageUSD = tt.weekly, tt.weeklyUse
			sub.DailyLimitUSD, sub.DailyUsageUSD = tt.daily, tt.dailyUse
			require.Equal(t, tt.want, sub.HighestQuotaExhausted())
		})
	}
}
