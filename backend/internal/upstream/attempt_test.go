package upstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// TestCapturePerformanceRetainsCompletedDisconnectDrain distinguishes a client write
// failure from an incomplete upstream read. Complete drained usage keeps throughput,
// whereas conversion failures and streams without terminal evidence cannot estimate it.
func TestCapturePerformanceRetainsCompletedDisconnectDrain(t *testing.T) {
	for _, tc := range []struct {
		name         string // completion fixture identity
		disconnected bool   // client write failure observation
		complete     bool   // successful upstream terminal observation
		expectTPS    bool   // expected throughput eligibility
	}{{"drained", true, true, true}, {"partial", true, false, false}, {"conversion_failed", false, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := timing.NewPerformance(timing.FirstOutput, 0)
			p.Stream, p.Complete, p.UsageComplete = true, tc.complete, true
			p.Values["upstream_first_output_ms"] = 100
			p.Values["upstream_last_output_ms"] = 600
			p.Values["upstream_output_event_count"] = 2
			ctx := timing.WithPerformance(t.Context(), p)
			result := AttemptResult{ClientDisconnect: tc.disconnected}
			result.CapturePerformance(ctx, context.Canceled)
			require.Equal(t, tc.expectTPS, result.Timings.TPS(10) != nil)
			require.True(t, p.UsageComplete)
		})
	}
}
