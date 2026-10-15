package gateway

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// execution.go binds prepared Qoder requests to scheduling callbacks and
// invokes the request execution loop. The executor creates a new output tracker
// and result accumulator for each call, allowing shared runtime dependencies to
// serve concurrent requests with separate attempt observations.

// RequestMetadata contains values captured by the HTTP adapter before execution.
type RequestMetadata = execution.RequestMetadata

// FundingState contains authenticated access data and the selected
// subscription after composite-group resolution.
// Billing performs funding checks and settlement authorization.
type FundingState = execution.FundingState

// ExecutionResult retains the final executed attempt and its observed usage
// even when execution fails.
type ExecutionResult = execution.ExecutionResult

// QoderRuntime supplies request preparation, funding checks, provider
// selection, and recovery decisions.
type QoderRuntime interface {
	// Prepare returns the request after adapter routing and input preparation.
	// The caller stops on error before admission or provider selection; retained
	// request references remain valid throughout the subsequent execution call.
	Prepare(context.Context, Request) (Request, error)
	// Check performs funding admission for the prepared request. afterWait
	// identifies the user-slot recheck so runtime implementations can avoid
	// counting request RPM again while validating funds after a wait.
	Check(context.Context, Request, bool) error
	// Select returns a usable provider selection outside the request's excluded
	// set. A successful selection supplies its executor, input and owned slot or
	// wait plan; errors stop or exhaust the outer provider loop.
	Select(context.Context, Request, map[int64]struct{}) (*Selection, error)
	// CanRefresh reports whether an upstream error permits credential refresh
	// recovery.
	CanRefresh(error) bool
	// CanFailover reports whether an upstream error permits another provider
	// attempt.
	CanFailover(error) bool
	// RefreshPending reports whether credential refresh is still owned by another
	// operation.
	RefreshPending(error) bool
	// QueueFailure synchronously reports a failed wait-counter operation for
	// request diagnostics.
	QueueFailure(string, error)
}

// ExecutionObserver synchronously reports request observations and wait heartbeats.
type ExecutionObserver interface {
	// Prepared synchronously observes adapter-prepared request state before
	// funding and selection. The request retains nested references; observers
	// copy data they need after the current execution rather than changing those
	// inputs.
	Prepared(Request)
	// Selected synchronously records the provider about to begin its attempt
	// execution.
	Selected(provider.ProviderSnapshot)
	// Waiting returns optional synchronous heartbeat hooks for a named slot wait.
	// The execution loop invokes these during acquisition; callback errors can
	// stop the wait through the scheduler's observation handling.
	Waiting(string) scheduler.WaitObserver
}

// NewQoderExecutor binds a usable Qoder runtime and optional concurrency
// service to its provider budget and wait timeout. Limits are stored as
// supplied; each Execute call later allocates its own request state.
func NewQoderExecutor(maxProviders int, waitTimeout time.Duration, concurrency *scheduler.ConcurrencyService, runtime QoderRuntime) *QoderUseCase {
	return &QoderUseCase{MaxProviders: maxProviders, WaitTimeout: waitTimeout, concurrency: concurrency, runtime: runtime}
}

// Execute prepares one Qoder request and delegates admission, provider
// attempts, and completion through the bound runtime. It requires a nonnil
// context, usable runtime, and output sink; pre-canceled calls skip
// preparation, and observed partial results can accompany an error.
func (u *QoderUseCase) Execute(ctx context.Context, request Request, output upstream.OutputSink) (ExecutionResult, error) {
	var result ExecutionResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	prepared, err := u.runtime.Prepare(ctx, request)
	if err != nil {
		return result, err
	}
	observer, _ := output.(ExecutionObserver)
	if observer != nil {
		observer.Prepared(prepared)
	}
	ports := RequestPorts{Concurrency: u.concurrency, CanRefresh: u.runtime.CanRefresh, CanFailover: u.runtime.CanFailover, RefreshPending: u.runtime.RefreshPending, QueueFailure: u.runtime.QueueFailure}
	ports.Check = func(ctx context.Context, afterWait bool) error { return u.runtime.Check(ctx, prepared, afterWait) }
	ports.Select = func(ctx context.Context, excluded map[int64]struct{}) (*Selection, error) {
		selected, err := u.runtime.Select(ctx, prepared, excluded)
		if err != nil {
			return nil, err
		}
		return selected, nil
	}
	if observer != nil {
		ports.WaitObserver = observer.Waiting
	}
	tracker := &OutputTracker{Sink: output}
	err = u.run(ctx, prepared, ports, tracker, &result)
	return result, err
}
