package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// fixedExecutionRuntime counts calls through one shared runtime to detect
// leaked state between requests.
type fixedExecutionRuntime struct {
	// prepares, checks, completed, and released count calls across concurrent
	// requests using this one runtime. Atomics keep their observations separate
	// from each request's model, payload, and result stored by the execution loop.
	prepares  atomic.Int64 // completed request preparation calls
	checks    atomic.Int64 // completed funding admission checks
	completed atomic.Int64 // completed usage callback invocations
	released  atomic.Int64 // completed provider release callbacks
	// execute is configured before requests begin and then retained by selected
	// executor functions; tests keep the callback stable during concurrent execution.
	execute func(context.Context, upstream.AttemptInput, upstream.OutputSink) (upstream.AttemptResult, error)
}

// fixedExecutionFunc delegates a selected attempt to a configured fixture
// callback.
type fixedExecutionFunc func(context.Context, upstream.AttemptInput, upstream.OutputSink) (upstream.AttemptResult, error)

// discardExecutionOutput accepts headers and events for execution observations
// without downstream I/O.
type discardExecutionOutput struct{}

// Prepare counts the request and returns its supplied value unchanged.
func (r *fixedExecutionRuntime) Prepare(_ context.Context, request Request) (Request, error) {
	r.prepares.Add(1)
	return request, nil
}

// Check counts the admission call and accepts the fixture request.
func (r *fixedExecutionRuntime) Check(context.Context, Request, bool) error {
	r.checks.Add(1)
	return nil
}

// Select returns an acquired fixture using UserID as provider identity and
// retaining request body/model input. Release and Complete update shared
// atomic counts; the configured execute callback performs each attempt.
func (r *fixedExecutionRuntime) Select(_ context.Context, request Request, _ map[int64]struct{}) (*Selection, error) {
	return &Selection{Acquired: true, Snapshot: provider.ProviderSnapshot{ID: request.UserID}, Input: upstream.AttemptInput{Body: request.Body, ResponseModel: request.Model}, Executor: fixedExecutionFunc(r.execute), Release: func() { r.released.Add(1) }, Complete: func(context.Context, upstream.AttemptResult) { r.completed.Add(1) }}, nil
}

// CanRefresh rejects credential refresh recovery for this execution fixture.
func (*fixedExecutionRuntime) CanRefresh(error) bool { return false }

// CanFailover rejects switching to another provider in this execution fixture.
func (*fixedExecutionRuntime) CanFailover(error) bool { return false }

// RefreshPending reports no pending credential refresh for this execution
// fixture.
func (*fixedExecutionRuntime) RefreshPending(error) bool { return false }

// QueueFailure discards wait-counter diagnostics for the fixed execution
// fixture.
func (*fixedExecutionRuntime) QueueFailure(string, error) {}

// Execute invokes the configured callback with this selected attempt and sink.
func (f fixedExecutionFunc) Execute(ctx context.Context, in upstream.AttemptInput, out upstream.OutputSink) (upstream.AttemptResult, error) {
	return f(ctx, in, out)
}

// Begin accepts the response head for execution tests using discarded output.
func (discardExecutionOutput) Begin(upstream.OutputHead) error { return nil }

// Emit accepts the output event for execution tests using discarded output.
func (discardExecutionOutput) Emit(upstream.OutputEvent) error { return nil }

// TestExecuteRetainsPartialFailureAndCompletesOnce serves partial output with
// usage before an upstream error. Execution must retain those observations,
// invoke completion once, and release its provider slot once.
func TestExecuteRetainsPartialFailureAndCompletesOnce(t *testing.T) {
	upstreamErr := errors.New("upstream failed after output")
	runtime := &fixedExecutionRuntime{execute: func(_ context.Context, in upstream.AttemptInput, out upstream.OutputSink) (upstream.AttemptResult, error) {
		require.NoError(t, out.Emit(upstream.OutputEvent{Data: []byte("partial"), CommitForRetry: true, Semantic: true}))
		return upstream.AttemptResult{Model: in.ResponseModel, Served: true, HasUsage: true, Usage: upstream.TokenUsage{OutputTokens: 3}}, upstreamErr
	}}
	core := NewQoderExecutor(3, time.Second, nil, runtime)
	result, err := core.Execute(context.Background(), Request{UserID: 7, Model: "model", Stream: true}, discardExecutionOutput{})
	require.ErrorIs(t, err, upstreamErr)
	require.Equal(t, 1, result.Attempts)
	require.Equal(t, int64(7), result.Provider.ID)
	require.Equal(t, 3, result.Attempt.Usage.OutputTokens)
	require.Equal(t, int64(1), runtime.completed.Load())
	require.Equal(t, int64(1), runtime.released.Load())
}

// TestExecuteCanceledBeforePreparation starts with an already canceled request
// context. Execute must return cancellation before runtime preparation,
// funding checks, or any upstream attempt is recorded.
func TestExecuteCanceledBeforePreparation(t *testing.T) {
	runtime := &fixedExecutionRuntime{}
	core := NewQoderExecutor(3, time.Second, nil, runtime)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := core.Execute(ctx, Request{}, discardExecutionOutput{})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, result.Attempts)
	require.Zero(t, runtime.prepares.Load())
	require.Zero(t, runtime.checks.Load())
}

// TestExecuteFixedDependenciesKeepRequestsIndependent runs two request
// identities against one shared runtime concurrently. Each result must retain
// its own model and payload while shared completion and release counts reach
// two.
func TestExecuteFixedDependenciesKeepRequestsIndependent(t *testing.T) {
	runtime := &fixedExecutionRuntime{execute: func(_ context.Context, in upstream.AttemptInput, _ upstream.OutputSink) (upstream.AttemptResult, error) {
		return upstream.AttemptResult{Model: in.ResponseModel, RequestID: string(in.Body)}, nil
	}}
	core := NewQoderExecutor(3, time.Second, nil, runtime)
	var workers sync.WaitGroup
	// Run both requests concurrently against the fixed runtime, then wait for
	// their return before reading the shared callback counts and per-request
	// identity assertions.
	for _, model := range []string{"first", "second"} {
		workers.Go(func() {
			result, err := core.Execute(context.Background(), Request{Model: model, Body: []byte(model)}, discardExecutionOutput{})
			if err != nil {
				t.Error(err)
				return
			}
			if result.Attempt.Model != model || result.Attempt.RequestID != model {
				t.Errorf("request changed: %+v", result)
			}
		})
	}
	workers.Wait()
	require.Equal(t, int64(2), runtime.completed.Load())
	require.Equal(t, int64(2), runtime.released.Load())
}
