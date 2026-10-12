package completion

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// SnapshotContext copies request identity, model mappings, and request capture into a background context.
func SnapshotContext(source context.Context) context.Context {
	return copyCompletionContext(context.Background(), source)
}

func copyCompletionContext(destination, source context.Context) context.Context {
	if destination == nil {
		destination = context.Background()
	}
	if source == nil {
		return destination
	}
	for _, key := range []telemetry.ContextKey{telemetry.RequestID, telemetry.ClientModel} {
		if value := source.Value(key); value != nil {
			destination = context.WithValue(destination, key, value)
		}
	}
	if trace, ok := modeltrace.FromContext(source); ok {
		snapshot := trace.Snapshot()
		destination = modeltrace.WithContext(destination, snapshot)
		if strings.TrimSpace(snapshot.ClientModel) != "" {
			destination = context.WithValue(destination, telemetry.ClientModel, snapshot.ClientModel)
		}
	}
	return telemetry.CopyRequestCapture(destination, source)
}

// WrapTaskContext snapshots source values at submission and retains the worker context cancellation and deadline.
func WrapTaskContext(source context.Context, task func(context.Context)) func(context.Context) {
	if task == nil {
		return nil
	}
	snapshot := SnapshotContext(source)
	return func(ctx context.Context) { task(copyCompletionContext(ctx, snapshot)) }
}
