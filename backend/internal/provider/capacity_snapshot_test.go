package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestObservedCapacityRetainsWindowBoundaryAndIndependentValues checks window times and copied runtime values at observation time.
func TestObservedCapacityRetainsWindowBoundaryAndIndependentValues(t *testing.T) {
	now := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
	row := GroupProviderCapacityRow{ProviderID: 9, Platform: PlatformOpenAI, Concurrency: 2, Extra: map[string]any{"max_sessions": 4, "session_idle_timeout_minutes": 3, "base_rpm": 12, "codex_7d_used_percent": 99.0, "codex_7d_reset_at": now.Add(time.Hour).Format(time.RFC3339), "codex_usage_updated_at": now.Format(time.RFC3339)}}
	settings := QuotaAutoPauseSettings{DefaultThreshold7d: .9}
	before := ProjectObservedCapacity(row, settings, now)
	require.True(t, before.QuotaAutoPaused)
	require.Equal(t, 4, before.MaxSessions)
	require.Equal(t, 3, before.SessionIdleTimeoutMinutes)
	require.Equal(t, 12, before.BaseRPM)
	atReset := ProjectObservedCapacity(row, settings, now.Add(time.Hour))
	require.False(t, atReset.QuotaAutoPaused)
	row.Extra["max_sessions"] = 8
	require.Equal(t, 4, before.MaxSessions)
}
