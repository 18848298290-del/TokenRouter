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
