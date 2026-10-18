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
