package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	FailureBilling        FailureStage = "billing"
	FailureUserQueue      FailureStage = "user_queue"
	FailureUserSlot       FailureStage = "user_slot"
	FailureProviderSlot   FailureStage = "provider_slot"
	FailureSelection      FailureStage = "selection"
	FailureExhausted      FailureStage = "exhausted"
	FailureRefreshPending FailureStage = "refresh_pending"
	FailureUpstream       FailureStage = "upstream"
)

// Request carries the authenticated subject and final routing plan.
type Request = execution.Request

type FailureStage string

// Failure holds the cause and failed stage; the HTTP adapter maps protocol status codes.
type Failure struct {
	Stage     FailureStage
	Cause     error
	Attempted bool
}

// Selection provides the current execution target and attempt callbacks.
type Selection struct {
	WaitWithoutCounter bool
	Snapshot           provider.ProviderSnapshot
	Plan               routing.CandidatePlan
	Acquired           bool
	Release            func()
	WaitPlan           *scheduler.ProviderWaitPlan
	Input              upstream.AttemptInput
	Executor           upstream.Executor
	Refresh            func(context.Context) (*Selection, error)
	Observe            func(upstream.AttemptResult, error)
	Bind               func(context.Context, upstream.AttemptResult)
	Complete           func(context.Context, upstream.AttemptResult)
	Switched           func()
}

// RequestPorts binds request services; Check(afterWait) rechecks funds after waiting, with RPM counted before waiting.
type RequestPorts struct {
	Concurrency    *scheduler.ConcurrencyService
	Check          func(context.Context, bool) error
	Select         func(context.Context, map[int64]struct{}) (*Selection, error)
	CanRefresh     func(error) bool
	CanFailover    func(error) bool
	RefreshPending func(error) bool
	WaitObserver   func(string) scheduler.WaitObserver
	QueueFailure   func(string, error)
}

// QoderUseCase holds execution dependencies and limits; Run keeps per-request state locally.
type QoderUseCase struct {
	MaxProviders int
	WaitTimeout  time.Duration
	Enter        func() (func(), error)
	concurrency  *scheduler.ConcurrencyService
	runtime      QoderRuntime
}

func (e *Failure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Stage)
}
func (e *Failure) Unwrap() error { return e.Cause }

func NewQoderUseCase(maxProviders int, waitTimeout time.Duration) *QoderUseCase {
	return &QoderUseCase{MaxProviders: maxProviders, WaitTimeout: waitTimeout}
}

// Run manages admission, leases, provider attempts, and completion through one provider loop.
// @project-doc docs/architecture/gateway_request_lifecycle.md#qoder_gateway_execution
func (u *QoderUseCase) Run(ctx context.Context, request Request, ports RequestPorts, output *OutputTracker) error {
	return u.run(ctx, request, ports, output, nil)
}

// run executes the provider loop with optional result capture.
func (u *QoderUseCase) run(ctx context.Context, request Request, ports RequestPorts, output *OutputTracker, execution *ExecutionResult) error {
	if u.Enter != nil {
		done, err := u.Enter()
		if err != nil {
			return err
		}
		defer done()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ports.Check != nil {
		if err := ports.Check(ctx, false); err != nil {
			return &Failure{Stage: FailureBilling, Cause: err}
		}
	}
	mode := scheduler.ReleaseOnCancel
	if request.Stream {
		mode = scheduler.ReleaseOnCompletion
	}
	lease := scheduler.NewLease(ctx, mode)
	defer lease.Release()
	if ports.Concurrency != nil {
		// Reserve a user wait-queue position before the first slot acquisition.
		waiting, waitErr := ports.Concurrency.EnterUserWait(ctx, request.UserID, scheduler.CalculateMaxWait(request.Concurrency))
		defer waiting.Release()
		if waitErr != nil {
			if ports.QueueFailure != nil {
				ports.QueueFailure("user", waitErr)
			}
		} else if !waiting.Allowed {
			return &Failure{Stage: FailureUserQueue, Cause: &scheduler.WaitQueueFullError{SlotType: "user"}}
		}
		observer := scheduler.WaitObserver{}
		if ports.WaitObserver != nil {
			observer = ports.WaitObserver("user")
		}
		waited := false
		begin := observer.Begin
		observer.Begin = func() error {
			waited = true
			if begin != nil {
				return begin()
			}
			return nil
		}
		keyID := int64(0)
		if request.Access != nil {
			keyID = request.Access.KeyID
		}
		userLease, _, err := ports.Concurrency.AcquireUser(ctx, scheduler.UserAcquireOptions{UserID: request.UserID, APIKeyID: keyID, Limit: request.Concurrency, Timeout: u.WaitTimeout, Mode: scheduler.ReleaseOnCompletion, Observer: observer})
		if err != nil {
			return &Failure{Stage: FailureUserSlot, Cause: err}
		}
		lease.Own(userLease.Release)
		waiting.Release()
		if waited && ports.Check != nil {
			if err := ports.Check(ctx, true); err != nil {
				return &Failure{Stage: FailureBilling, Cause: err}
			}
		}
	}
	ctx = scheduler.WithRequestLease(ctx, lease)
	excluded := make(map[int64]struct{})
	var lastErr error
	refreshPending := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		selected, err := ports.Select(ctx, excluded)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if refreshPending {
				return &Failure{Stage: FailureRefreshPending, Cause: err, Attempted: true}
			}
			if len(excluded) == 0 {
				return &Failure{Stage: FailureSelection, Cause: err}
			}
			if lastErr != nil {
				err = lastErr
			}
			return &Failure{Stage: FailureExhausted, Cause: err, Attempted: true}
		}
		refreshPending = false
		result, attemptErr := u.attempt(ctx, request, ports, lease, selected, output, execution)
		if selected.Observe != nil {
			selected.Observe(result, attemptErr)
		}
		if attemptErr != nil && result.Served && result.HasUsage {
			if selected.Complete != nil {
				selected.Complete(ctx, result)
			}
			return &Failure{Stage: FailureUpstream, Cause: attemptErr, Attempted: true}
		}
		if attemptErr != nil && (errors.Is(attemptErr, context.Canceled) || ctx.Err() != nil) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return attemptErr
		}
		var slotErr *Failure
		if errors.As(attemptErr, &slotErr) {
			return slotErr
		}
		if attemptErr != nil && !output.AttemptCommitted && selected.Refresh != nil && ports.CanRefresh != nil && ports.CanRefresh(attemptErr) {
			refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			refreshed, refreshErr := selected.Refresh(refreshCtx)
			cancel()
			if refreshErr == nil && refreshed != nil {
				selected = refreshed
				result, attemptErr = u.attempt(ctx, request, ports, lease, selected, output, execution)
				if selected.Observe != nil {
					selected.Observe(result, attemptErr)
				}
				if attemptErr != nil && result.Served && result.HasUsage {
					if selected.Complete != nil {
						selected.Complete(ctx, result)
					}
					return &Failure{Stage: FailureUpstream, Cause: attemptErr, Attempted: true}
				}
				if attemptErr != nil && (errors.Is(attemptErr, context.Canceled) || ctx.Err() != nil) {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return attemptErr
				}
				if errors.As(attemptErr, &slotErr) {
					return slotErr
				}
			} else if ports.RefreshPending != nil && ports.RefreshPending(refreshErr) {
				if !output.AttemptCommitted {
					excluded[selected.Snapshot.ID] = struct{}{}
					if len(excluded) < u.MaxProviders {
						refreshPending = true
						if selected.Switched != nil {
							selected.Switched()
						}
						continue
					}
				}
				return &Failure{Stage: FailureRefreshPending, Cause: refreshErr, Attempted: true}
			}
		}
		if attemptErr == nil {
			if selected.Bind != nil {
				selected.Bind(ctx, result)
			}
			if selected.Complete != nil {
				selected.Complete(ctx, result)
			}
			return nil
		}
		if !output.AttemptCommitted && ports.CanFailover != nil && ports.CanFailover(attemptErr) {
			excluded[selected.Snapshot.ID] = struct{}{}
			if len(excluded) < u.MaxProviders {
				lastErr = attemptErr
				if selected.Switched != nil {
					selected.Switched()
				}
				continue
			}
		}
		return &Failure{Stage: FailureUpstream, Cause: attemptErr, Attempted: true}
	}
}

func (u *QoderUseCase) attempt(ctx context.Context, request Request, ports RequestPorts, parent *scheduler.Lease, selected *Selection, output *OutputTracker, execution *ExecutionResult) (upstream.AttemptResult, error) {
	if observer, ok := output.Sink.(ExecutionObserver); ok {
		observer.Selected(selected.Snapshot)
	}
	output.BeginAttempt()
	attempt := scheduler.NewAttemptLease(parent, nil, selected.Release)
	defer attempt.Release()
	if !selected.Acquired {
		if ports.Concurrency == nil || selected.WaitPlan == nil {
			return upstream.AttemptResult{}, &Failure{Stage: FailureProviderSlot, Cause: errors.New("no available providers")}
		}
		plan := selected.WaitPlan
		var waiting scheduler.WaitResult
		var err error
		if !selected.WaitWithoutCounter {
			waiting, err = ports.Concurrency.EnterProviderWait(ctx, selected.Snapshot.ID, plan.MaxWaiting)
		} else {
			waiting.Allowed = true
		}
		defer waiting.Release()
		if err != nil {
			if ports.QueueFailure != nil {
				ports.QueueFailure("provider", err)
			}
		} else if !waiting.Allowed {
			return upstream.AttemptResult{}, &Failure{Stage: FailureProviderSlot, Cause: &scheduler.WaitQueueFullError{SlotType: "provider"}}
		}
		observer := scheduler.WaitObserver{}
		if ports.WaitObserver != nil {
			observer = ports.WaitObserver("provider")
		}
		release, err := ports.Concurrency.WaitForSlot(ctx, "provider", selected.Snapshot.ID, plan.MaxConcurrency, plan.Timeout, true, observer)
		if err != nil {
			return upstream.AttemptResult{}, &Failure{Stage: FailureProviderSlot, Cause: err}
		}
		waiting.Release()
		attempt = scheduler.NewAttemptLease(parent, nil, release)
		defer attempt.Release()
	}
	if err := ctx.Err(); err != nil {
		return upstream.AttemptResult{}, err
	}
	result, err := selected.Executor.Execute(ctx, selected.Input, output)
	if execution != nil {
		execution.Attempts++
		execution.Attempt = result
		execution.Provider = selected.Snapshot
		execution.Plan = selected.Plan
		execution.PlanProvided = selected.Plan.ProviderID != 0
	}
	attempt.Finish(scheduler.AttemptOutcome{Served: result.Served})
	return result, err
}
