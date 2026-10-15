package completion

import (
	"context"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/modeltrace"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// context.go captures completion correlation values and model traces for
// execution after a request ends. Completion work needs the request's
// identifiers and model chain after the request context has been cancelled. The
// model trace is captured at submission, so a later WebSocket turn cannot
// change the models attached to an earlier task. The request recorder is
// deliberately shared because background usage supplements the same request
// summary that the entrypoint finishes.
//
// The worker supplies cancellation and its execution deadline. Selected request
// values are layered onto that context, while SnapshotContext starts with a
// background context when callers need detached capture. Arbitrary source values
// are retained only by the separate inline-submission policy.

// SnapshotContext captures correlation values in a context detached from the
// source's cancellation and deadline. It copies the model trace and shares the
// request recorder; a nil source yields a background context.
func SnapshotContext(source context.Context) context.Context {
	return copyCompletionContext(context.Background(), source)
}

// copyCompletionContext combines completion values with the destination's
// cancellation and deadline. A nil destination becomes a background context, and
// a nil source leaves the destination unchanged.
//
// RequestID and ClientModel are retained as supplied values. A copied model trace
// can replace ClientModel with its nonblank client name, while CopyRequestCapture
// retains the same synchronized recorder so background model updates reach the
// entrypoint's request summary. Callers choose the destination that owns the task's
// execution budget.
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
	// Capture the trace before later turns register models; its client name also
	// replaces older ClientModel evidence carried by the source context.
	if trace, ok := modeltrace.FromContext(source); ok {
		snapshot := trace.Snapshot()
		destination = modeltrace.WithContext(destination, snapshot)
		if strings.TrimSpace(snapshot.ClientModel) != "" {
			destination = context.WithValue(destination, telemetry.ClientModel, snapshot.ClientModel)
		}
	}
	return telemetry.CopyRequestCapture(destination, source)
}

// WrapTaskContext captures request values immediately and returns a task using
// the eventual worker context for cancellation and deadlines. Nil tasks remain
// nil; the wrapper shares request-record updates but isolates the model trace.
func WrapTaskContext(source context.Context, task func(context.Context)) func(context.Context) {
	if task == nil {
		return nil
	}
	snapshot := SnapshotContext(source)
	return func(ctx context.Context) { task(copyCompletionContext(ctx, snapshot)) }
}
