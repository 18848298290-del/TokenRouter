package kimi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The fixture contains two details but must produce one five-hour window:
//   - The first limits[].detail supplies five-hour usage.
//   - usage supplies weekly usage.
//   - Subsequent details must not create duplicate windows.
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
	require.InDelta(t, 40.0, tiers[0].UsedPercent, 1e-9) // (1000-600)/1000*100
	require.Equal(t, "2026-08-14T15:00:00Z", tiers[0].ResetAt)
	require.Equal(t, "7d", tiers[1].Window)
	require.InDelta(t, 60.0, tiers[1].UsedPercent, 1e-9) // (10000-4000)/10000*100
	require.Equal(t, "2026-08-18T00:00:00Z", tiers[1].ResetAt)
}

// A zero limit reports zero utilization without division by zero.
func TestParseKimiUsageTiers_LimitZero(t *testing.T) {
	t.Parallel()
	body := []byte(`{"limits":[{"detail":{"limit":0,"remaining":0,"resetTime":"2026-08-14T15:00:00Z"}}]}`)
	tiers := ParseKimiUsageTiers(body)
	require.Len(t, tiers, 1)
	require.InDelta(t, 0.0, tiers[0].UsedPercent, 1e-9)
}
