package billing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// TestMemberQuotaCalendarProjection checks DST day boundaries and resets expired windows in a copy while retaining
// current windows.
func TestMemberQuotaCalendarProjection(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	calendar := timezone.NewCalendar(location)
	now := time.Date(2026, time.November, 1, 7, 30, 0, 0, time.UTC)
	stale := time.Date(2026, time.October, 31, 0, 0, 0, 0, location)
	week := calendar.StartOfWeek(now)
	source := MemberQuotaSnapshot{DailyLimitUSD: 5, WeeklyLimitUSD: 9, MonthlyLimitUSD: 30, DailyUsageUSD: 5, WeeklyUsageUSD: 7, MonthlyUsageUSD: 29, DailyWindowStart: &stale, WeeklyWindowStart: &week, MonthlyWindowStart: &stale}
	got := NormalizeMemberQuotaWindows(source, now, calendar)
	require.Zero(t, got.DailyUsageUSD)
	require.Equal(t, float64(7), got.WeeklyUsageUSD)
	require.Zero(t, got.MonthlyUsageUSD)
	require.Equal(t, calendar.StartOfDay(now), *got.DailyWindowStart)
	require.Same(t, source.WeeklyWindowStart, got.WeeklyWindowStart)
	require.Equal(t, float64(5), source.DailyUsageUSD)
	require.Equal(t, stale, *source.DailyWindowStart)
	require.Equal(t, 25*time.Hour, calendar.StartOfDay(now).AddDate(0, 0, 1).Sub(calendar.StartOfDay(now)))
	require.NoError(t, CheckMemberQuotaSnapshot(got))
}

// TestMemberQuotaThresholdOrder checks daily, weekly, then monthly rejection and unlimited zero limits.
func TestMemberQuotaThresholdOrder(t *testing.T) {
	snapshot := MemberQuotaSnapshot{DailyLimitUSD: 1, WeeklyLimitUSD: 2, MonthlyLimitUSD: 3, DailyUsageUSD: 1, WeeklyUsageUSD: 2, MonthlyUsageUSD: 3}
	require.ErrorIs(t, CheckMemberQuotaSnapshot(snapshot), ErrTeamMemberDailyExceeded)
	snapshot.DailyLimitUSD = 0
	require.ErrorIs(t, CheckMemberQuotaSnapshot(snapshot), ErrTeamMemberWeeklyExceeded)
	snapshot.WeeklyLimitUSD = 0
	require.ErrorIs(t, CheckMemberQuotaSnapshot(snapshot), ErrTeamMemberMonthlyExceeded)
	snapshot.MonthlyLimitUSD = 0
	require.NoError(t, CheckMemberQuotaSnapshot(snapshot))
}
