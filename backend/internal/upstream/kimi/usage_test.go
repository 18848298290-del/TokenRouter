package kimi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Two coding-limit details in one response must produce one five-hour
// observation, followed by the weekly observation. The fixtures check the
// selected percentages and reset strings, plus the zero-utilization result when
// the selected limit is zero.

// TestParseKimiUsageTiers supplies two detail entries and weekly usage with
// different percentages. It checks that the first detail determines the
// five-hour value and reset, while usage determines the weekly observation.
func TestParseKimiUsageTiers(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"limits": [
			{"name": "5h", "detail": {"limit": 1000, "remaining": 600, "resetTime": "2026-08-14T15:00:00Z"}},
			{"name": "ignored-second-detail", "detail": {"limit": 999, "remaining": 0, "resetTime": "2026-08-14T20:00:00Z"}}
		],
		"usage": {"limit": 10000, "remaining": 4000, "resetTime": "2026-08-18T00:00:00Z"}
	}`)
	tiers := ParseKimiUsageTiers(body)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	// Use a small absolute tolerance for floating-point percentage
	// arithmetic.
	require.InDelta(t, 40.0, tiers[0].UsedPercent, 1e-9)
	require.Equal(t, "2026-08-14T15:00:00Z", tiers[0].ResetAt)
	require.Equal(t, "7d", tiers[1].Window)
	// Compare the weekly percentage using the same floating-point
	// tolerance.
	require.InDelta(t, 60.0, tiers[1].UsedPercent, 1e-9)
	require.Equal(t, "2026-08-18T00:00:00Z", tiers[1].ResetAt)
}

// TestParseKimiUsageTiers_LimitZero supplies a zero limit and zero remaining
// amount in one detail. It checks that the parser emits one window with zero
// utilization.
func TestParseKimiUsageTiers_LimitZero(t *testing.T) {
	t.Parallel()
	body := []byte(`{"limits":[{"detail":{"limit":0,"remaining":0,"resetTime":"2026-08-14T15:00:00Z"}}]}`)
	tiers := ParseKimiUsageTiers(body)
	require.Len(t, tiers, 1)
	// Check zero utilization with the percentage assertion's absolute
	// tolerance.
	require.InDelta(t, 0.0, tiers[0].UsedPercent, 1e-9)
}
