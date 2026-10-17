package completion

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// TestUsagePerformanceDroppedSubmissionDoesNotCreateUsage fills the existing worker
// queue under its drop policy. Rejection must remain diagnostic and must not execute
// the completion callback or change the request's successful HTTP outcome.
func TestUsagePerformanceDroppedSubmissionDoesNotCreateUsage(t *testing.T) {
	pool := NewUsageRecordWorkerPoolWithOptions(UsageRecordWorkerPoolOptions{WorkerCount: 1, QueueSize: 1, OverflowPolicy: "drop"})
	entered, release := make(chan struct{}), make(chan struct{})
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) {
		close(entered)
		<-release
	}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	require.Equal(t, UsageRecordSubmitModeEnqueued, pool.Submit(func(context.Context) { <-release }))
	var saved telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "request", State: "completed"}, func(record telemetry.RequestRecord) { saved = record })
	p := timing.NewPerformance(timing.FirstOutput, 10)
	p.Values["request_body_read_ms"] = 4
	p.Values["upstream_first_output_ms"] = 20
	ctx = timing.WithPerformance(ctx, p)
	SubmitTask(pool, ctx, func(context.Context) { t.Error("dropped completion executed") }, false, SubmissionOptions{})
	require.Equal(t, "not_persisted", saved.UsageWriteState)
	require.Equal(t, "completed", saved.State)
	require.Equal(t, int64(4), saved.FailureTimings["request_body_read_ms"])
	require.NotContains(t, saved.FailureTimings, "upstream_first_output_ms")
	close(release)
	stop, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, pool.StopContext(stop))
}

// TestSubmitTaskTerminalDiagnostics covers early return and task panic around storage.
// A panic before acknowledgement remains unknown, while one after a confirmed write
// preserves that confirmation and reports the independent task failure.
func TestSubmitTaskTerminalDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name         string // submitted task scenario
		acknowledged bool   // write completes before panic
		panicTask    bool   // task interrupts completion
		state        string // expected persistence disposition
	}{{"early_return", false, false, "skipped"}, {"panic_before_write", false, true, "unknown"}, {"panic_after_write", true, true, "persisted"}} {
		t.Run(tc.name, func(t *testing.T) {
			var saved telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "request"}, func(record telemetry.RequestRecord) { saved = record })
			p := timing.NewPerformance(timing.FirstOutput, 10)
			ctx = timing.WithPerformance(ctx, p)
			SubmitTask(nil, ctx, func(taskCtx context.Context) {
				if tc.acknowledged {
					observeUsageWrite(taskCtx, "persisted", "", p)
				}
				if tc.panicTask {
					panic("completion interrupted")
				}
			}, false, SubmissionOptions{RecoverPanic: true})
			require.Equal(t, tc.state, saved.UsageWriteState)
			require.NotEmpty(t, saved.UsageWriteError)
			if tc.acknowledged {
				require.Nil(t, saved.FailureTimings)
			} else {
				require.Equal(t, int64(10), saved.FailureTimings["upstream_started_ms"])
			}
		})
	}
}
