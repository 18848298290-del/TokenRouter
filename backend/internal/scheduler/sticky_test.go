package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// TestDefaultOpenAIProviderScheduler_ShouldEscapeStickyProvider_ThresholdBoundary
// supplies a TTFT sample exactly at its threshold and evolves error feedback
// through successes and failures. It checks that equality remains accepted and
// verifies the expected EWMA values after the additional failures.
func TestDefaultOpenAIProviderScheduler_ShouldEscapeStickyProvider_ThresholdBoundary(t *testing.T) {
	stats := NewRuntimeStats(time.Now)
	providerID := int64(21501)
	ttft := 15000
	stats.Report(providerID, true, &ttft)
	stats.Report(providerID, false, nil)
	stats.Report(providerID, true, nil)
	reason, errorRate, observedTTFT, shouldEscape := ShouldEscapeSticky(stats, providerID, policy.StickyEscapeConfig{
		Enabled:   true,
		TtftMs:    15000,
		ErrorRate: 0.5,
	})
	require.False(t, shouldEscape)
	require.Empty(t, reason)
	require.InDelta(t, 0.16, errorRate, 1e-9)
	require.InDelta(t, 15000, observedTTFT, 1e-9)

	for range 4 {
		stats.Report(providerID, false, nil)
	}
	reason, errorRate, _, shouldEscape = ShouldEscapeSticky(stats, providerID, policy.StickyEscapeConfig{
		Enabled:   true,
		TtftMs:    15000,
		ErrorRate: 1,
	})
	require.False(t, shouldEscape)
	require.Empty(t, reason)
	reason, errorRate, observedTTFT, shouldEscape = ShouldEscapeSticky(stats, providerID, policy.StickyEscapeConfig{
		Enabled:   true,
		TtftMs:    15000,
		ErrorRate: errorRate,
	})
	require.False(t, shouldEscape)
	require.Empty(t, reason)
	require.InDelta(t, 0.655936, errorRate, 1e-9)
	require.InDelta(t, 15000, observedTTFT, 1e-9)
}
