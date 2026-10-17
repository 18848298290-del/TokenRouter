package completion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// usageWriterSpy accepts usage while request observers expose the updated summary.
type usageWriterSpy struct{}

// performanceWriteFixture reports controlled write results without performing billing.
type performanceWriteFixture struct {
	bestError   error         // acknowledged or uncertain first result
	createError error         // synchronous fallback result
	entered     chan struct{} // optional write admission signal
	release     chan struct{} // optional acknowledgement barrier
	panicWrite  bool          // simulate interrupted writer execution
}

// Create accepts the supplied usage row for lifecycle-observation tests and
// returns successful creation without retaining or persisting any fixture data.
func (usageWriterSpy) Create(context.Context, *UsageLog) (bool, error) { return true, nil }

// TestWriteUsagePreservesRequestLifecycle supplements HTTP and child-turn captures
// in running and terminal states. It checks unchanged timing, outcome, and status,
// with client display model, provider identity, and billing/upstream aliases added by usage.
func TestWriteUsagePreservesRequestLifecycle(t *testing.T) {
	for _, state := range []string{"running", "completed", "failed", "canceled"} {
		for _, child := range []bool{false, true} {
			name := state
			if child {
				name += "/turn"
			}
			t.Run(name, func(t *testing.T) {
				finished := time.Now().Add(-time.Second)
				initial := telemetry.RequestRecord{
					RequestID: "request", StartedAt: finished.Add(-62 * time.Second), State: state,
					Method: "POST", Path: "/v1/responses", FailureTimings: map[string]int64{"provider_slot_acquired_ms": 60000},
				}
				if state != "running" {
					initial.FinishedAt = &finished
					initial.DurationMs = 62000
					initial.Status = 200
				}
				var latest telemetry.RequestRecord
				observe := func(record telemetry.RequestRecord) { latest = record }
				ctx := context.WithValue(t.Context(), telemetry.RequestID, initial.RequestID)
				ctx = telemetry.WithRequestCapture(ctx, initial, observe)
				if child {
					initial.RequestID = "turn"
					ctx = telemetry.WithChildRequest(ctx, initial)
				}
				ctx = context.WithValue(ctx, telemetry.ClientModel, "client-model")
				recorder := NewRecorder(Dependencies{Logs: usageWriterSpy{}, RequestRecords: observe}, RecorderOptions{})
				duration := 2000
				upstream := "upstream-id"
				row := &UsageLog{RequestID: initial.RequestID, BillingKey: "billing", RequestedModel: "upstream-model", ProviderID: 7, Platform: "openai", DurationMs: &duration, UpstreamRequestID: &upstream}
				WrapTaskContext(ctx, func(taskCtx context.Context) { recorder.WriteUsage(taskCtx, row, "test") })(context.Background())
				require.Equal(t, initial.StartedAt, latest.StartedAt)
				require.Equal(t, initial.FinishedAt, latest.FinishedAt)
				require.Equal(t, initial.DurationMs, latest.DurationMs)
				require.Equal(t, initial.State, latest.State)
				require.Equal(t, initial.Status, latest.Status)
				require.Equal(t, initial.FailureTimings, latest.FailureTimings)
				require.Equal(t, "client-model", latest.Model)
				require.Equal(t, int64(7), latest.ProviderID)
				require.Contains(t, latest.Aliases, telemetry.RequestAlias{Kind: "billing", Value: "billing"})
				require.Contains(t, latest.Aliases, telemetry.RequestAlias{Kind: "upstream", Value: upstream})
			})
		}
	}
}

// TestWriteUsageKeepsTaskSeparateFromPollingRequest supplies a task usage ID to
// a different running polling capture. It checks a separate completed task record
// and a still-running parent with no task financial aliases added.
func TestWriteUsageKeepsTaskSeparateFromPollingRequest(t *testing.T) {
	var parent, task telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "poll", State: "running"}, func(record telemetry.RequestRecord) { parent = record })
	recorder := NewRecorder(Dependencies{Logs: usageWriterSpy{}, RequestRecords: func(record telemetry.RequestRecord) { task = record }}, RecorderOptions{})
	recorder.WriteUsage(ctx, &UsageLog{RequestID: "task", BillingKey: "task-billing"}, "test")
	require.Equal(t, "running", parent.State)
	require.Empty(t, parent.Aliases)
	require.Equal(t, "task", task.RequestID)
	require.Equal(t, "completed", task.State)
	require.NotNil(t, task.FinishedAt)
}

// Create returns the configured fallback outcome without changing the supplied row.
func (f *performanceWriteFixture) Create(context.Context, *UsageLog) (bool, error) {
	return f.createError == nil, f.createError
}

// CreateBestEffort holds its acknowledgement until the test releases the writer.
// The caller's row remains immutable while the simulated database owns the write.
func (f *performanceWriteFixture) CreateBestEffort(context.Context, *UsageLog) error {
	if f.panicWrite {
		panic("writer interrupted")
	}
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	return f.bestError
}

// TestUsagePerformancePersistenceEvidence separates SQL acknowledgement from task
// submission. Unknown first writes remain unknown when a confirmed-unwritten retry
// fails, and successful fallback never copies performance into request diagnostics.
func TestUsagePerformancePersistenceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string // fixture scenario name
		best     error  // first writer result
		fallback error  // fallback writer result
		state    string // expected diagnostic outcome
	}{
		{"acknowledged", nil, nil, "persisted"},
		{"fallback_success", errors.New("initial write failed"), nil, "persisted"},
		{"confirmed_unwritten", usage.MarkUsageLogCreateNotPersisted(errors.New("rejected")), usage.MarkUsageLogCreateNotPersisted(errors.New("stopped")), "not_persisted"},
		{"late_write_possible", usage.MarkUsageLogCreateDropped(context.DeadlineExceeded), usage.MarkUsageLogCreateNotPersisted(errors.New("stopped")), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "request", State: "completed"}, func(record telemetry.RequestRecord) { saved = record })
			p := timing.NewPerformance(timing.FirstOutput, 10)
			p.Values["attempt_number"] = 2
			p.Values["upstream_first_output_ms"] = 20
			p.Values["upstream_last_output_ms"] = 120
			row := &UsageLog{RequestID: "request", OutputTokens: 10, Timings: p}
			recorder := NewRecorder(Dependencies{Logs: &performanceWriteFixture{bestError: tc.best, createError: tc.fallback}}, RecorderOptions{})
			recorder.WriteUsage(ctx, row, "test")
			require.Equal(t, tc.state, saved.UsageWriteState)
			require.Equal(t, int64(2), saved.UsageAttempt)
			require.Equal(t, "completed", saved.State)
			if tc.state == "persisted" {
				require.Nil(t, saved.FailureTimings)
			} else {
				require.Equal(t, int64(10), saved.FailureTimings["upstream_started_ms"])
				require.NotContains(t, saved.FailureTimings, "upstream_first_output_ms")
				require.NotContains(t, saved.FailureTimings, "upstream_last_output_ms")
			}
			require.NotNil(t, saved.UsageWriteDurationMs)
			require.Equal(t, int64(120), row.Timings.Values["upstream_last_output_ms"])
		})
	}
}

// TestUsagePerformanceWaitsForAcknowledgement pauses an admitted usage write while
// the request is already complete. Pending evidence must survive until SQL acknowledgement,
// and the request summary must never become a temporary performance backup.
func TestUsagePerformanceWaitsForAcknowledgement(t *testing.T) {
	var mu sync.Mutex
	var saved telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "request", State: "completed"}, func(record telemetry.RequestRecord) {
		mu.Lock()
		saved = record
		mu.Unlock()
	})
	writer := &performanceWriteFixture{entered: make(chan struct{}), release: make(chan struct{})}
	recorder := NewRecorder(Dependencies{Logs: writer}, RecorderOptions{})
	row := &UsageLog{RequestID: "request", Timings: timing.NewPerformance(timing.FirstOutput, 0)}
	observeUsageWrite(ctx, "pending", "", row.Timings)
	done := make(chan struct{})
	go func() {
		defer close(done)
		recorder.WriteUsage(ctx, row, "test")
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	mu.Lock()
	state, copied := saved.UsageWriteState, saved.FailureTimings
	mu.Unlock()
	require.Equal(t, "pending", state)
	require.Nil(t, copied)
	close(writer.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("write did not finish")
	}
	mu.Lock()
	state, copied = saved.UsageWriteState, saved.FailureTimings
	mu.Unlock()
	require.Equal(t, "persisted", state)
	require.Nil(t, copied)
}

// TestWriteUsagePanicCannotAcknowledgePersistence interrupts the writer before any result.
// The summary remains unknown and retains transport diagnostics while the panic follows
// the caller's existing recovery policy, never manufacturing a confirmed database write.
func TestWriteUsagePanicCannotAcknowledgePersistence(t *testing.T) {
	var saved telemetry.RequestRecord
	ctx := telemetry.WithRequestCapture(t.Context(), telemetry.RequestRecord{RequestID: "request"}, func(record telemetry.RequestRecord) { saved = record })
	p := timing.NewPerformance(timing.FirstOutput, 10)
	recorder := NewRecorder(Dependencies{Logs: &performanceWriteFixture{panicWrite: true}}, RecorderOptions{})
	require.Panics(t, func() { recorder.WriteUsage(ctx, &UsageLog{RequestID: "request", Timings: p}, "test") })
	require.Equal(t, "unknown", saved.UsageWriteState)
	require.Equal(t, int64(10), saved.FailureTimings["upstream_started_ms"])
}

// TestWriteUsageKeepsDiagnosticHeaderAndStoredValueSeparate writes an already
// truncated configured usage identifier after full diagnostic capture. The usage
// value stays unchanged and does not create a second unclassified request alias.
func TestWriteUsageKeepsDiagnosticHeaderAndStoredValueSeparate(t *testing.T) {
	for _, withAttempt := range []bool{false, true} {
		full := strings.Repeat("a", 200)
		stored := full[:128]
		var latest telemetry.RequestRecord
		observe := func(record telemetry.RequestRecord) { latest = record }
		initial := telemetry.RequestRecord{
			RequestID: "request", ProviderID: 7,
			Attempts: []telemetry.RequestAttempt{{Number: 1, ProviderID: 7, RequestID: full, RequestIDHeader: "X-Custom-ID"}},
			Aliases:  []telemetry.RequestAlias{{Kind: "upstream_request", Value: full, Source: "X-Custom-ID", ProviderID: 7, AttemptNumber: 1}},
		}
		if !withAttempt {
			initial.Attempts = nil
		}
		ctx := telemetry.WithRequestCapture(t.Context(), initial, observe)
		recorder := NewRecorder(Dependencies{Logs: usageWriterSpy{}, RequestRecords: observe}, RecorderOptions{})
		row := &UsageLog{RequestID: "request", ProviderID: 7, UpstreamRequestID: &stored}
		recorder.WriteUsage(ctx, row, "test")
		require.Equal(t, strings.Repeat("a", 128), *row.UpstreamRequestID)
		require.Equal(t, []telemetry.RequestAlias{{Kind: "upstream_request", Value: full, Source: "X-Custom-ID", ProviderID: 7, AttemptNumber: 1}}, latest.Aliases)
	}
}

// TestWriteUsageSuppressesOnlyLinkedDiagnosticAliases compares a recorded child
// against unrelated tasks and providers. Existing usage values stay unchanged,
// and only proven parent-child diagnostic coverage suppresses a legacy alias.
func TestWriteUsageSuppressesOnlyLinkedDiagnosticAliases(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, providerID := range []int64{7, 8} {
			initial := telemetry.RequestRecord{RequestID: "parent", Attempts: []telemetry.RequestAttempt{{Number: 1, ProviderID: 7, RequestIDHeader: "X-Custom", RequestID: "full"}}}
			if linked {
				initial.Aliases = []telemetry.RequestAlias{{Kind: "related", Value: "child"}}
			}
			var task telemetry.RequestRecord
			ctx := telemetry.WithRequestCapture(t.Context(), initial, nil)
			recorder := NewRecorder(Dependencies{Logs: usageWriterSpy{}, RequestRecords: func(r telemetry.RequestRecord) { task = r }}, RecorderOptions{})
			value := "stored"
			row := &UsageLog{RequestID: "child", ProviderID: providerID, UpstreamRequestID: &value}
			recorder.WriteUsage(ctx, row, "test")
			require.Equal(t, "stored", *row.UpstreamRequestID)
			legacy := telemetry.RequestAlias{Kind: "upstream", Value: "stored"}
			if linked && providerID == 7 {
				require.NotContains(t, task.Aliases, legacy)
			} else {
				require.Contains(t, task.Aliases, legacy)
			}
		}
	}
}
