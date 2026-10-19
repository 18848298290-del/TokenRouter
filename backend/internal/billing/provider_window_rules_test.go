package billing

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFixedProviderWindowRequiresStrictlyPositiveLimit supplies zero,
// negative, and NaN limits to fixed-mode reconciliation. It checks
// that they do not reset, then verifies the UTC midnight boundary
// for a positive limit and retention when stored start equals it.
func TestFixedProviderWindowRequiresStrictlyPositiveLimit(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, limit := range []float64{0, -1, math.NaN()} {
		_, reset := ReconcileProviderFixedWindow(FixedProviderWindow{Mode: "fixed", Limit: limit}, time.Time{}, time.UTC, now)
		require.False(t, reset)
	}
	boundary, reset := ReconcileProviderFixedWindow(FixedProviderWindow{Mode: "fixed", Limit: 1, Hour: 0}, time.Time{}, time.UTC, now)
	require.True(t, reset)
	require.Equal(t, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), boundary)
	_, reset = ReconcileProviderFixedWindow(FixedProviderWindow{Mode: "fixed", Limit: 1}, boundary, time.UTC, now)
	require.False(t, reset, "窗口起点恰在边界时保留消费")
}

// TestFixedMonthlyResetCalendar exercises short months, leap years, year rollover,
// and a non-UTC reset clock. Each selected boundary retains the configured day
// in its target month and equality advances publication into the next month.
func TestFixedMonthlyResetCalendar(t *testing.T) {
	tz := time.FixedZone("UTC+8", 8*60*60)
	for _, tc := range []struct {
		name string // calendar scenario name
		now  string // local observation timestamp
		last string // current monthly reset
		next string // next monthly reset
	}{
		{"short February", "2026-02-28T09:00:00+08:00", "2026-02-28T09:00:00+08:00", "2026-03-31T09:00:00+08:00"},
		{"leap February", "2028-02-29T08:00:00+08:00", "2028-01-31T09:00:00+08:00", "2028-02-29T09:00:00+08:00"},
		{"year rollover", "2026-12-31T09:00:00+08:00", "2026-12-31T09:00:00+08:00", "2027-01-31T09:00:00+08:00"},
		{"before short month reset", "2026-04-30T08:00:00+08:00", "2026-03-31T09:00:00+08:00", "2026-04-30T09:00:00+08:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			window := FixedProviderWindow{Period: ProviderMonthlyWindow, Mode: "fixed", Limit: 100, Day: 31, Hour: 9}
			next, ok := NextProviderFixedReset(window, tz, now)
			require.True(t, ok)
			require.Equal(t, tc.next, next.In(tz).Format(time.RFC3339))
			last, reset := ReconcileProviderFixedWindow(window, time.Time{}, tz, now)
			require.True(t, reset)
			require.Equal(t, tc.last, last.In(tz).Format(time.RFC3339))
			_, reset = ReconcileProviderFixedWindow(window, last, tz, now)
			require.False(t, reset)
		})
	}
}

// TestProviderFixedResetClockTransitions checks skipped and repeated reset hours
// in northern and southern timezones. Every period selects the accounting reset
// instant and restores its configured hour in the following calendar period.
func TestProviderFixedResetClockTransitions(t *testing.T) {
	for _, tc := range []struct {
		name        string // clock transition scenario
		zone        string // configured IANA timezone
		hour        int    // configured reset hour
		reset       string // expected transition reset
		nextDaily   string // following daily reset
		nextWeekly  string // following weekly reset
		nextMonthly string // following monthly reset
	}{
		{"New York overlap", "America/New_York", 1, "2026-11-01T06:00:00Z", "2026-11-02T06:00:00Z", "2026-11-08T06:00:00Z", "2026-12-01T06:00:00Z"},
		{"New York gap", "America/New_York", 2, "2026-03-08T07:00:00Z", "2026-03-09T06:00:00Z", "2026-03-15T06:00:00Z", "2026-04-08T06:00:00Z"},
		{"Sydney overlap", "Australia/Sydney", 2, "2026-04-04T16:00:00Z", "2026-04-05T16:00:00Z", "2026-04-11T16:00:00Z", "2026-05-04T16:00:00Z"},
		{"Lord Howe half-hour gap", "Australia/Lord_Howe", 2, "2026-10-03T15:30:00Z", "2026-10-04T15:00:00Z", "2026-10-10T15:00:00Z", "2026-11-03T15:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			location, err := time.LoadLocation(tc.zone)
			require.NoError(t, err)
			reset, err := time.Parse(time.RFC3339, tc.reset)
			require.NoError(t, err)
			for _, period := range []struct {
				name   string               // quota period label
				period ProviderWindowPeriod // quota calculation selector
				day    int                  // configured reset day
				next   string               // following reset instant
			}{
				{"daily", ProviderDailyWindow, 1, tc.nextDaily},
				{"weekly", ProviderWeeklyWindow, int(reset.In(location).Weekday()), tc.nextWeekly},
				{"monthly", ProviderMonthlyWindow, reset.In(location).Day(), tc.nextMonthly},
			} {
				t.Run(period.name, func(t *testing.T) {
					window := FixedProviderWindow{Period: period.period, Mode: "fixed", Limit: 100, Day: period.day, Hour: tc.hour}
					next, ok := NextProviderFixedReset(window, location, reset.Add(-time.Second))
					require.True(t, ok)
					require.Equal(t, tc.reset, next.UTC().Format(time.RFC3339))
					for _, now := range []time.Time{reset, reset.Add(time.Second)} {
						next, ok = NextProviderFixedReset(window, location, now)
						require.True(t, ok)
						require.Equal(t, period.next, next.UTC().Format(time.RFC3339))
						last, expired := ReconcileProviderFixedWindow(window, reset.Add(-time.Second), location, now)
						require.True(t, expired)
						require.Equal(t, tc.reset, last.UTC().Format(time.RFC3339))
						_, expired = ReconcileProviderFixedWindow(window, reset, location, now)
						require.False(t, expired)
					}
				})
			}
		})
	}
}

// TestFixedDailyResetSkippedDate supplies the morning after Apia skipped a
// calendar date. The previous window must precede observation even when the
// skipped date's configured hour normalizes into the future.
func TestFixedDailyResetSkippedDate(t *testing.T) {
	location, err := time.LoadLocation("Pacific/Apia")
	require.NoError(t, err)
	now, err := time.Parse(time.RFC3339, "2011-12-30T10:30:00Z")
	require.NoError(t, err)
	require.Equal(t, "2011-12-30T11:00:00Z", NextFixedDailyReset(1, location, now).UTC().Format(time.RFC3339))
	require.Equal(t, "2011-12-29T11:00:00Z", LastFixedDailyReset(1, location, now).UTC().Format(time.RFC3339))
}
