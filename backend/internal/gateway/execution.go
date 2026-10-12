package gateway

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// RequestMetadata contains values captured by the HTTP adapter before execution.
type RequestMetadata = execution.RequestMetadata

// FundingState contains authenticated access data and the selected subscription after composite-group resolution.
// Billing performs funding checks and settlement authorization.
type FundingState = execution.FundingState

// ExecutionResult retains the final executed attempt and its observed usage even when execution fails.
type ExecutionResult = execution.ExecutionResult

// QoderRuntime supplies request preparation, funding checks, provider selection, and recovery decisions.
type QoderRuntime interface {
	Prepare(context.Context, Request) (Request, error)
	Check(context.Context, Request, bool) error
	Select(context.Context, Request, map[int64]struct{}) (*Selection, error)
	CanRefresh(error) bool
	CanFailover(error) bool
	RefreshPending(error) bool
	QueueFailure(string, error)
}

// ExecutionObserver synchronously reports request observations and wait heartbeats.
type ExecutionObserver interface {
	Prepared(Request)
	Selected(provider.ProviderSnapshot)
	Waiting(string) scheduler.WaitObserver
}

// NewQoderExecutor binds Qoder runtime and concurrency dependencies.
func NewQoderExecutor(maxProviders int, waitTimeout time.Duration, concurrency *scheduler.ConcurrencyService, runtime QoderRuntime) *QoderUseCase {
	return &QoderUseCase{MaxProviders: maxProviders, WaitTimeout: waitTimeout, concurrency: concurrency, runtime: runtime}
}

// Execute prepares a request and runs provider selection, attempts, and completion with the bound runtime.
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
