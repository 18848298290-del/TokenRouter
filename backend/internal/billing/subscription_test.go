package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// TestSubscriptionCalendarBoundaries supplies New York's spring and autumn
// transition dates with stored daily anchors. It checks reset selection and
// next-midnight display across twenty-three/twenty-five-hour days, including
// the instant before and at reset.
func TestSubscriptionCalendarBoundaries(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	calendar := timezone.NewCalendar(location)
	for _, item := range []struct {
		name string // named transition scenario label
		// day and hours pair each New York transition midnight with its expected
		// next-midnight elapsed duration. The two fixtures exercise spring's shorter
		// day and autumn's longer day.
		day   time.Time     // selected transition date midnight
		hours time.Duration // expected elapsed calendar-day duration
	}{
		{"spring", time.Date(2026, 3, 8, 0, 0, 0, 0, location), 23 * time.Hour},
		{"autumn", time.Date(2026, 11, 1, 0, 0, 0, 0, location), 25 * time.Hour},
	} {
		t.Run(item.name, func(t *testing.T) {
			limit := 10.0
			previous := item.day.AddDate(0, 0, -1)
			sub := &UserSubscription{StartsAt: previous, ExpiresAt: item.day.AddDate(0, 0, 4), DailyWindowStart: &previous, DailyLimitUSD: &limit}
			start, reset := sub.AutomaticDailyWindowStartWithCalendar(item.day.Add(time.Hour), calendar)
			require.True(t, reset)
			require.True(t, start.Equal(item.day))
			sub.DailyWindowStart = &item.day
			next := sub.DailyResetTime(calendar)
			require.NotNil(t, next)
			require.Equal(t, item.hours, next.Sub(item.day))
			// The instant before next midnight stays in the earlier calendar day;
			// equality moves daily maintenance into the new day.
			require.False(t, sub.NeedsDailyResetAt(next.Add(-time.Nanosecond), calendar))
			require.True(t, sub.NeedsDailyResetAt(*next, calendar))
		})
	}
}

// TestCalculateSubscriptionRemaining_IgnoresDisabledZeroLimit gives a zero
// weekly limit with large recorded weekly use. Display remaining quota must
// use the daily seven-dollar remainder rather than that disabled period.
func TestCalculateSubscriptionRemaining_IgnoresDisabledZeroLimit(t *testing.T) {
	sub := &UserSubscription{
		DailyLimitUSD:   subscriptionLimitPtr(10),
		WeeklyLimitUSD:  subscriptionLimitPtr(0),
		MonthlyLimitUSD: subscriptionLimitPtr(100),
		DailyUsageUSD:   3,
		WeeklyUsageUSD:  99,
		MonthlyUsageUSD: 20,
	}

	require.Equal(t, 7.0, SubscriptionRemainingForDisplay(sub))
}

// TestCalculateSubscriptionRemaining_NoPositiveLimitsReturnsUnlimited supplies
// zero and absent limits to the display helper. It checks negative one
// represents entirely unlimited quota instead of an exhausted zero remainder.
func TestCalculateSubscriptionRemaining_NoPositiveLimitsReturnsUnlimited(t *testing.T) {
	sub := &UserSubscription{
		DailyLimitUSD:   subscriptionLimitPtr(0),
		MonthlyLimitUSD: subscriptionLimitPtr(0),
	}

	require.Equal(t, -1.0, SubscriptionRemainingForDisplay(sub))
}

// TestUserSubscriptionDaysRemainingAt evaluates expiry before, at and around
// exact twenty-four-hour multiples from a fixed instant. It checks positive
// partial days round upward while elapsed lifetimes return zero.
func TestUserSubscriptionDaysRemainingAt(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string    // named expiry-rounding scenario label
		expiresAt time.Time // supplied grant expiry instant
		want      int       // expected upward-rounded day count
	}{
		{name: "expired", expiresAt: now.Add(-time.Nanosecond), want: 0},
		{name: "expires now", expiresAt: now, want: 0},
		{name: "less than one day", expiresAt: now.Add(SubscriptionDailyWindow - time.Nanosecond), want: 1},
		{name: "exactly one day", expiresAt: now.Add(SubscriptionDailyWindow), want: 1},
		{name: "over one day", expiresAt: now.Add(SubscriptionDailyWindow + time.Nanosecond), want: 2},
		{name: "less than two days", expiresAt: now.Add(2*SubscriptionDailyWindow - time.Nanosecond), want: 2},
		{name: "exactly two days", expiresAt: now.Add(2 * SubscriptionDailyWindow), want: 2},
		{name: "over two days", expiresAt: now.Add(2*SubscriptionDailyWindow + time.Nanosecond), want: 3},
		{name: "exactly seven days", expiresAt: now.Add(7 * SubscriptionDailyWindow), want: 7},
		{name: "over seven days", expiresAt: now.Add(7*SubscriptionDailyWindow + time.Nanosecond), want: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &UserSubscription{ExpiresAt: tt.expiresAt}
			require.Equal(t, tt.want, sub.DaysRemainingAt(now))
		})
	}
}

// TestUserSubscriptionAvailableQuotaUSD_IgnoresDisabledZeroLimit combines
// enabled daily/monthly quotas and a zero weekly quota with large recorded
// use. It checks weekly remainder is nil and the smallest enabled remainder is
// seven dollars.
func TestUserSubscriptionAvailableQuotaUSD_IgnoresDisabledZeroLimit(t *testing.T) {
	sub := &UserSubscription{
		DailyLimitUSD:   quotaLimitPtr(10),
		WeeklyLimitUSD:  quotaLimitPtr(0),
		MonthlyLimitUSD: quotaLimitPtr(100),
		DailyUsageUSD:   3,
		WeeklyUsageUSD:  99,
		MonthlyUsageUSD: 20,
	}

	require.NotNil(t, sub.RemainingDailyUSD())
	require.Nil(t, sub.RemainingWeeklyUSD())
	require.NotNil(t, sub.RemainingMonthlyUSD())
	require.Equal(t, 7.0, sub.AvailableQuotaUSD())
}

// TestUserSubscriptionAvailableQuotaUSD_NoPositiveLimitsReturnsZero supplies
// absent or zero limits to the arithmetic quota helper. All per-period
// remainders must be nil and the selected numeric result zero.
func TestUserSubscriptionAvailableQuotaUSD_NoPositiveLimitsReturnsZero(t *testing.T) {
	sub := &UserSubscription{
		DailyLimitUSD:   quotaLimitPtr(0),
		MonthlyLimitUSD: quotaLimitPtr(0),
	}

	require.Nil(t, sub.RemainingDailyUSD())
	require.Nil(t, sub.RemainingWeeklyUSD())
	require.Nil(t, sub.RemainingMonthlyUSD())
	require.Equal(t, 0.0, sub.AvailableQuotaUSD())
}

// TestUserSubscriptionEffectiveStatus_DeletedAtReturnsRevoked places an
// active-label grant inside its time interval with a deletion marker. Derived
// status must be revoked and the stored-active predicate false.
func TestUserSubscriptionEffectiveStatus_DeletedAtReturnsRevoked(t *testing.T) {
	now := time.Now().UTC()
	deletedAt := now.Add(-time.Minute)
	sub := &UserSubscription{
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(time.Hour),
		Status:    SubscriptionStatusActive,
		DeletedAt: &deletedAt,
	}

	require.Equal(t, SubscriptionStatusRevoked, sub.EffectiveStatus(now))
	require.False(t, sub.IsActive())
}

// subscriptionLimitPtr returns fresh scalar storage for a display-test quota
// amount.
func subscriptionLimitPtr(v float64) *float64 {
	return &v
}

// quotaLimitPtr returns fresh scalar storage for a remainder-test quota
// amount.
func quotaLimitPtr(v float64) *float64 {
	return &v
}
