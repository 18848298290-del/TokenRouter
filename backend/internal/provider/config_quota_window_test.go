package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// The next fixed reset must be strictly after the supplied observation, while
// current-window reconciliation keeps consumption at an already current start.
// Daily and weekly scenarios check those distinctions across local weekdays,
// UTC conversion, rolling metadata removal, and billing's invalid-hour
// normalization.

// TestNextFixedDailyReset_BeforeResetHour supplies a time before the local
// daily reset hour. The next boundary remains on the same calendar date at that
// configured hour.
func TestNextFixedDailyReset_BeforeResetHour(t *testing.T) {
	tz := time.UTC
	// At 06:00 UTC, the daily 09:00 reset is still ahead.
	after := time.Date(2026, 3, 14, 6, 0, 0, 0, tz)
	got := billing.NextFixedDailyReset(9, tz, after)
	want := time.Date(2026, 3, 14, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedDailyReset_AtResetHour supplies the exact local daily reset
// instant. Next-reset publication advances one calendar day because the
// supplied occurrence has already taken effect.
func TestNextFixedDailyReset_AtResetHour(t *testing.T) {
	tz := time.UTC
	// At the reset instant, choose tomorrow's reset.
	after := time.Date(2026, 3, 14, 9, 0, 0, 0, tz)
	got := billing.NextFixedDailyReset(9, tz, after)
	want := time.Date(2026, 3, 15, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedDailyReset_AfterResetHour supplies a time after the local daily
// reset. The next boundary advances to tomorrow's configured reset hour instead
// of returning the elapsed occurrence.
func TestNextFixedDailyReset_AfterResetHour(t *testing.T) {
	tz := time.UTC
	// After the reset instant, choose tomorrow's reset.
	after := time.Date(2026, 3, 14, 15, 30, 0, 0, tz)
	got := billing.NextFixedDailyReset(9, tz, after)
	want := time.Date(2026, 3, 15, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedDailyReset_MidnightReset evaluates a midnight schedule just
// before the day ends. The result is midnight on the following calendar date,
// protecting date rollover for this schedule.
func TestNextFixedDailyReset_MidnightReset(t *testing.T) {
	tz := time.UTC
	// At 23:59, the next midnight reset is tomorrow.
	after := time.Date(2026, 3, 14, 23, 59, 0, 0, tz)
	got := billing.NextFixedDailyReset(0, tz, after)
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedDailyReset_NonUTCTimezone supplies a UTC instant whose Shanghai
// local reset has passed. The returned boundary uses the following Shanghai
// calendar date and configured local reset hour.
func TestNextFixedDailyReset_NonUTCTimezone(t *testing.T) {
	tz, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	// 07:00 UTC is 15:00 in Shanghai, after the local 09:00 reset.
	after := time.Date(2026, 3, 14, 7, 0, 0, 0, time.UTC)
	got := billing.NextFixedDailyReset(9, tz, after)
	// The next local reset is 2026-03-15 01:00 UTC.
	want := time.Date(2026, 3, 15, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedWeeklyReset_TargetDayAhead supplies Saturday with a Monday
// morning weekly schedule. The returned next reset is the following Monday,
// maintaining the requested weekday and hour.
func TestNextFixedWeeklyReset_TargetDayAhead(t *testing.T) {
	tz := time.UTC
	// From Saturday, choose the next Monday at 09:00.
	after := time.Date(2026, 3, 14, 10, 0, 0, 0, tz)
	got := billing.NextFixedWeeklyReset(1, 9, tz, after)
	// The next Monday is 2026-03-16.
	want := time.Date(2026, 3, 16, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedWeeklyReset_TargetDayToday_BeforeHour supplies Monday before its
// configured reset hour. The next weekly reset stays on that same Monday
// because the occurrence is still ahead.
func TestNextFixedWeeklyReset_TargetDayToday_BeforeHour(t *testing.T) {
	tz := time.UTC
	// On Monday before 09:00, use today's reset.
	after := time.Date(2026, 3, 16, 6, 0, 0, 0, tz)
	got := billing.NextFixedWeeklyReset(1, 9, tz, after)
	// Today's reset is at 09:00.
	want := time.Date(2026, 3, 16, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedWeeklyReset_TargetDayToday_AtHour supplies the exact Monday
// weekly reset instant. Publication chooses the following Monday so the result
// represents a future occurrence.
func TestNextFixedWeeklyReset_TargetDayToday_AtHour(t *testing.T) {
	tz := time.UTC
	// At Monday's reset instant, advance one week.
	after := time.Date(2026, 3, 16, 9, 0, 0, 0, tz)
	got := billing.NextFixedWeeklyReset(1, 9, tz, after)
	// The next reset is next Monday at 09:00.
	want := time.Date(2026, 3, 23, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedWeeklyReset_TargetDayToday_AfterHour supplies Monday after its
// configured weekly reset. The next boundary advances a full calendar week to
// the following Monday morning.
func TestNextFixedWeeklyReset_TargetDayToday_AfterHour(t *testing.T) {
	tz := time.UTC
	// After Monday's reset, advance one week.
	after := time.Date(2026, 3, 16, 15, 0, 0, 0, tz)
	got := billing.NextFixedWeeklyReset(1, 9, tz, after)
	// The next reset is next Monday at 09:00.
	want := time.Date(2026, 3, 23, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedWeeklyReset_TargetDayPast supplies Wednesday with a Monday reset
// schedule. The next reset uses the following Monday, moving ahead to the next
// matching weekday.
func TestNextFixedWeeklyReset_TargetDayPast(t *testing.T) {
	tz := time.UTC
	// From Wednesday, choose the following Monday.
	after := time.Date(2026, 3, 18, 10, 0, 0, 0, tz)
	got := billing.NextFixedWeeklyReset(1, 9, tz, after)
	// The next Monday is 2026-03-23.
	want := time.Date(2026, 3, 23, 9, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNextFixedWeeklyReset_Sunday supplies Saturday for a Sunday-midnight
// weekly schedule. Weekday zero produces the following Sunday's boundary,
// protecting the Sunday encoding used by stored configuration.
func TestNextFixedWeeklyReset_Sunday(t *testing.T) {
	tz := time.UTC
	// From Saturday, choose the next Sunday reset.
	after := time.Date(2026, 3, 14, 10, 0, 0, 0, tz)
	got := billing.NextFixedWeeklyReset(0, 0, tz, after)
	// The next Sunday is 2026-03-15.
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, tz)
	assert.Equal(t, want, got)
}

// TestNormalizeFixedQuotaWindows_ClearsExpiredWeeklyWindow starts saved
// consumption before the current UTC Monday window. Reconciliation clears
// weekly usage and writes the current Monday start in RFC3339 form.
func TestNormalizeFixedQuotaWindows_ClearsExpiredWeeklyWindow(t *testing.T) {
	now := time.Now().UTC()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	currentWeekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	staleStart := currentWeekStart.Add(-24 * time.Hour)
	extra := map[string]any{
		"quota_weekly_limit":      500.0,
		"quota_weekly_used":       76.0,
		"quota_weekly_start":      staleStart.Format(time.RFC3339),
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1),
		"quota_weekly_reset_hour": float64(0),
		"quota_reset_timezone":    "UTC",
	}

	NormalizeFixedQuotaWindows(extra, time.Now(), time.LoadLocation)

	assert.Equal(t, 0.0, extra["quota_weekly_used"])
	assert.Equal(t, currentWeekStart.Format(time.RFC3339), extra["quota_weekly_start"])
}

// TestNormalizeFixedQuotaWindows_KeepsActiveWeeklyWindow supplies consumption
// whose saved start already matches the current UTC Monday. Reconciliation
// keeps the amount and start because the stored window remains current.
func TestNormalizeFixedQuotaWindows_KeepsActiveWeeklyWindow(t *testing.T) {
	now := time.Now().UTC()
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	currentWeekStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysSinceMonday)
	extra := map[string]any{
		"quota_weekly_limit":      500.0,
		"quota_weekly_used":       76.0,
		"quota_weekly_start":      currentWeekStart.Format(time.RFC3339),
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1),
		"quota_weekly_reset_hour": float64(0),
		"quota_reset_timezone":    "UTC",
	}

	NormalizeFixedQuotaWindows(extra, time.Now(), time.LoadLocation)

	assert.Equal(t, 76.0, extra["quota_weekly_used"])
	assert.Equal(t, currentWeekStart.Format(time.RFC3339), extra["quota_weekly_start"])
}

// TestComputeQuotaResetAt_RollingMode_NoResetAt supplies rolling daily and
// weekly schedules with no reset metadata. Publication leaves both reset_at
// keys absent because those dimensions use elapsed durations.
func TestComputeQuotaResetAt_RollingMode_NoResetAt(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode":  "rolling",
		"quota_weekly_reset_mode": "rolling",
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	_, hasDailyResetAt := extra["quota_daily_reset_at"]
	_, hasWeeklyResetAt := extra["quota_weekly_reset_at"]
	assert.False(t, hasDailyResetAt, "rolling mode should not set quota_daily_reset_at")
	assert.False(t, hasWeeklyResetAt, "rolling mode should not set quota_weekly_reset_at")
}

// TestComputeQuotaResetAt_RollingMode_ClearsExistingResetAt supplies rolling
// schedules alongside old fixed reset timestamps. Both timestamps are removed,
// keeping management metadata consistent with the selected rolling modes.
func TestComputeQuotaResetAt_RollingMode_ClearsExistingResetAt(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode":  "rolling",
		"quota_weekly_reset_mode": "rolling",
		"quota_daily_reset_at":    "2026-03-14T09:00:00Z",
		"quota_weekly_reset_at":   "2026-03-16T09:00:00Z",
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	_, hasDailyResetAt := extra["quota_daily_reset_at"]
	_, hasWeeklyResetAt := extra["quota_weekly_reset_at"]
	assert.False(t, hasDailyResetAt, "rolling mode should remove quota_daily_reset_at")
	assert.False(t, hasWeeklyResetAt, "rolling mode should remove quota_weekly_reset_at")
}

// TestComputeQuotaResetAt_FixedDaily_SetsResetAt publishes a fixed daily
// schedule with a UTC morning hour. The saved RFC3339 timestamp parses, lies in
// the future at assertion time, and retains that UTC hour.
func TestComputeQuotaResetAt_FixedDaily_SetsResetAt(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(9),
		"quota_reset_timezone":   "UTC",
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	resetAtStr, ok := extra["quota_daily_reset_at"].(string)
	require.True(t, ok, "quota_daily_reset_at should be set")

	resetAt, err := time.Parse(time.RFC3339, resetAtStr)
	require.NoError(t, err)
	// The reset is in the future.
	assert.True(t, resetAt.After(time.Now()), "reset_at should be in the future")
	// The reset hour is 09:00 UTC.
	assert.Equal(t, 9, resetAt.UTC().Hour())
}

// TestComputeQuotaResetAt_FixedWeekly_SetsResetAt publishes a fixed Monday
// schedule in UTC. The saved timestamp parses, lies in the future at assertion
// time, and falls on Monday.
func TestComputeQuotaResetAt_FixedWeekly_SetsResetAt(t *testing.T) {
	extra := map[string]any{
		"quota_weekly_reset_mode": "fixed",
		"quota_weekly_reset_day":  float64(1), // Monday
		"quota_weekly_reset_hour": float64(0),
		"quota_reset_timezone":    "UTC",
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	resetAtStr, ok := extra["quota_weekly_reset_at"].(string)
	require.True(t, ok, "quota_weekly_reset_at should be set")

	resetAt, err := time.Parse(time.RFC3339, resetAtStr)
	require.NoError(t, err)
	// The reset is in the future.
	assert.True(t, resetAt.After(time.Now()), "reset_at should be in the future")
	// The reset falls on Monday.
	assert.Equal(t, time.Monday, resetAt.UTC().Weekday())
}

// TestComputeQuotaResetAt_FixedDaily_WithTimezone publishes a morning reset
// using the Shanghai timezone. Parsing the stored timestamp and converting it
// to that location recovers the configured local hour.
func TestComputeQuotaResetAt_FixedDaily_WithTimezone(t *testing.T) {
	tz, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	extra := map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(9),
		"quota_reset_timezone":   "Asia/Shanghai",
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	resetAtStr, ok := extra["quota_daily_reset_at"].(string)
	require.True(t, ok)

	resetAt, err := time.Parse(time.RFC3339, resetAtStr)
	require.NoError(t, err)
	// The reset hour is 09:00 in Shanghai.
	assert.Equal(t, 9, resetAt.In(tz).Hour())
}

// TestComputeQuotaResetAt_DefaultTimezone omits quota_reset_timezone from a
// fixed daily schedule. The timestamp published into Extra uses UTC and retains
// the configured noon reset hour after parsing.
func TestComputeQuotaResetAt_DefaultTimezone(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(12),
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	resetAtStr, ok := extra["quota_daily_reset_at"].(string)
	require.True(t, ok)

	resetAt, err := time.Parse(time.RFC3339, resetAtStr)
	require.NoError(t, err)
	// The default time zone is UTC.
	assert.Equal(t, 12, resetAt.UTC().Hour())
}

// TestComputeQuotaResetAt_InvalidHour_ClampedToZero supplies an out-of-range
// daily reset hour to publication. Billing's wrapper normalizes it to midnight,
// and the resulting UTC timestamp has hour zero.
func TestComputeQuotaResetAt_InvalidHour_ClampedToZero(t *testing.T) {
	extra := map[string]any{
		"quota_daily_reset_mode": "fixed",
		"quota_daily_reset_hour": float64(99),
		"quota_reset_timezone":   "UTC",
	}
	ComputeQuotaResetAt(extra, time.Now(), time.LoadLocation)
	resetAtStr, ok := extra["quota_daily_reset_at"].(string)
	require.True(t, ok)

	resetAt, err := time.Parse(time.RFC3339, resetAtStr)
	require.NoError(t, err)
	// Publication normalizes an invalid clock hour to midnight before calculation.
	assert.Equal(t, 0, resetAt.UTC().Hour())
}
