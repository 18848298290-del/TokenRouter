package completion

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// usageWriterSpy accepts usage while request observers expose the updated summary.
type usageWriterSpy struct{}

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
					Method: "POST", Path: "/v1/responses", Timings: map[string]int64{"provider_slot_acquired_ms": 60000},
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
				require.Equal(t, initial.Timings, latest.Timings)
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
