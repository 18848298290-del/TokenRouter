package completion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// TestCompletionContextSnapshotsModelTraceAndKeepsWorkerBudget captures a task
// before the source trace advances and is cancelled. It checks the earlier model
// chain, detached snapshot cancellation, and the cancelled worker context used at execution.
func TestCompletionContextSnapshotsModelTraceAndKeepsWorkerBudget(t *testing.T) {
	trace := modeltrace.NewAPIKeyModelRedirectTrace("client", "source", "first")
	source, cancelSource := context.WithCancel(modeltrace.WithContext(context.WithValue(context.Background(), telemetry.RequestID, "request-id"), trace))
	task := WrapTaskContext(source, func(worker context.Context) {
		require.ErrorIs(t, worker.Err(), context.Canceled)
		require.Equal(t, "request-id", worker.Value(telemetry.RequestID))
		value, ok := modeltrace.FromContext(worker)
		require.True(t, ok)
		require.NotSame(t, trace, value)
		require.Equal(t, []string{"first"}, value.ResponseModels())
	})
	// Advance and cancel the source after capture, then cancel a separate worker
	// context to distinguish retained values from the context that owns execution.
	trace.RegisterModel("next-turn")
	cancelSource()
	snapshot := SnapshotContext(source)
	require.NoError(t, snapshot.Err())
	worker, cancelWorker := context.WithCancel(context.Background())
	cancelWorker()
	task(worker)
}
