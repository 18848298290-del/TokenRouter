package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/execution"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// qoder.go coordinates Qoder request admission, provider attempts, credential
// refresh, and completion callbacks.
// Qoder request execution keeps funding admission and resource leases around a
// provider loop. A stream can continue collecting upstream usage after
// downstream cancellation, so its request lease is released on completion;
// ordinary requests additionally allow cancellation cleanup. Waiting ends
// before funds are checked again.
//
// A served partial result with observed usage enters completion once and ends
// recovery, keeping another provider from replaying billable work. Before
// retry commitment, credential refresh can create one additional attempt, and
// provider exclusions bound further failover. The attempt captures
// observations before releasing its provider lease; the outer loop then
// invokes completion or successful binding. Shared construction dependencies
// stay separate from this request's counters and output tracker.

// Failure stages identify admission, wait, selection, and upstream phases so
// the protocol adapter chooses its final response from the typed request failure.
const (
	FailureBilling        FailureStage = "billing"         // funding admission or recheck failure
	FailureUserQueue      FailureStage = "user_queue"      // user wait queue admission failure
	FailureUserSlot       FailureStage = "user_slot"       // user concurrency slot acquisition failure
	FailureProviderSlot   FailureStage = "provider_slot"   // provider slot or queue failure
	FailureSelection      FailureStage = "selection"       // initial provider selection failure
	FailureExhausted      FailureStage = "exhausted"       // provider exclusion set is exhausted
	FailureRefreshPending FailureStage = "refresh_pending" // credential refresh remains in progress
	FailureUpstream       FailureStage = "upstream"        // final upstream attempt execution failure
)

// Request carries the authenticated subject and final routing plan.
type Request = execution.Request

// FailureStage identifies the request phase where a typed failure occurred.
type FailureStage string

// Failure holds the cause and failed stage; the HTTP adapter maps protocol
// status codes.
type Failure struct {
	Stage     FailureStage // request phase that failed
	Cause     error        // underlying admission or upstream error
	Attempted bool         // an upstream attempt was made
}

// Selection provides the current execution target and attempt callbacks.
type Selection struct {
	WaitWithoutCounter bool                      // provider waiting bypasses queue counter
	Snapshot           provider.ProviderSnapshot // selected provider observation snapshot
	Plan               routing.CandidatePlan     // selected provider candidate route plan
	Acquired           bool                      // provider slot was already acquired
	// Release belongs to this selected attempt and is registered with its lease.
	// Pass nil when no slot is held; acquired selections provide their cleanup.
	Release  func()                                    // owned provider slot release callback
	WaitPlan *scheduler.ProviderWaitPlan               // optional provider slot wait instructions
	Input    upstream.AttemptInput                     // prepared selected upstream attempt input
	Executor upstream.Executor                         // selected platform single-attempt executor
	Refresh  func(context.Context) (*Selection, error) // optional credential refresh selection callback
	// Observe, Bind, and Complete are synchronous callbacks after attempt return.
	// Observe receives every result; success binds before completion, while a
	// served usage-bearing failure completes directly and ends the loop.
	Observe  func(upstream.AttemptResult, error)           // optional attempt result feedback callback
	Bind     func(context.Context, upstream.AttemptResult) // optional successful session binding callback
	Complete func(context.Context, upstream.AttemptResult) // optional completion recording callback
	Switched func()                                        // optional provider switch observation callback
}

// RequestPorts binds request services; Check(afterWait) rechecks funds after
// waiting, with RPM counted before waiting.
type RequestPorts struct {
	Concurrency    *scheduler.ConcurrencyService                                 // optional shared concurrency acquisition service
	Check          func(context.Context, bool) error                             // optional funding admission check callback
	Select         func(context.Context, map[int64]struct{}) (*Selection, error) // required provider selection callback
	CanRefresh     func(error) bool                                              // optional credential recovery eligibility callback
	CanFailover    func(error) bool                                              // optional provider switch eligibility callback
	RefreshPending func(error) bool                                              // optional pending refresh classification callback
	WaitObserver   func(string) scheduler.WaitObserver                           // optional slot wait heartbeat factory
	QueueFailure   func(string, error)                                           // optional wait counter failure observer
}

// QoderUseCase retains shared dependencies and limits while each call owns its
// loop state.
type QoderUseCase struct {
	MaxProviders int           // distinct excluded provider budget
	WaitTimeout  time.Duration // user slot wait timeout duration
	// Enter admits the request to application activity and returns cleanup.
	// Accepted cleanup remains owned until admission, attempts, and completion
	// return from the request loop.
	Enter       func() (func(), error)        // optional application request activity admission
	concurrency *scheduler.ConcurrencyService // bound shared concurrency acquisition service
	runtime     QoderRuntime                  // bound Qoder request preparation services
}

// Error returns Cause's message when present, otherwise the failed stage
// string. Callers keep the stored cause intact so protocol presentation can
// still unwrap and classify it.
func (e *Failure) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Stage)
}

// Unwrap exposes the underlying cause for standard error-chain classification.
func (e *Failure) Unwrap() error { return e.Cause }

// NewQoderUseCase stores provider and user-wait limits for callers supplying
// RequestPorts to Run. It binds no runtime or concurrency service; callers
// choosing Execute use NewQoderExecutor for those dependencies.
func NewQoderUseCase(maxProviders int, waitTimeout time.Duration) *QoderUseCase {
	return &QoderUseCase{MaxProviders: maxProviders, WaitTimeout: waitTimeout}
}

// Run delegates Qoder admission, leased attempts, and completion using
// caller-supplied request ports and output tracking. It requires a usable
// context, selection callback, and tracker; this facade omits optional
// ExecutionResult capture.
// @project-doc docs/architecture/gateway_request_lifecycle.md#qoder_gateway_execution
func (u *QoderUseCase) Run(ctx context.Context, request Request, ports RequestPorts, output *OutputTracker) error {
	return u.run(ctx, request, ports, output, nil)
}

// run executes the request loop with optional final-attempt capture. Callers
// supply a usable selection and sink, and application Enter returns cleanup
// when admission succeeds. Funding Check runs before selection and can repeat
// after a user wait.
//
// A completion-mode stream lease keeps acquired resources through result
// collection, while other requests permit cancellation cleanup. Served partial
// usage completes once before cancellation or recovery decisions. Refresh can
// retry one selection before commitment; pending refresh and ordinary failover
// exclude providers within MaxProviders. Successful results bind then
// complete, and every exit releases the request lease.
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
	// Keep stream resources until execution has collected tail usage; the
	// platform decides detached stream cancellation, while this lease governs
	// acquired request-slot cleanup.
	mode := scheduler.ReleaseOnCancel
	if request.Stream {
		mode = scheduler.ReleaseOnCompletion
	}
	lease := scheduler.NewLease(ctx, mode)
	defer lease.Release()
	if ports.Concurrency != nil {
		// Register the wait counter before user-slot acquisition and own cleanup
		// immediately, so rejected or canceled waits release any counter actually
		// acquired.
		waiting, waitErr := ports.Concurrency.EnterUserWait(ctx, request.UserID, scheduler.CalculateMaxWait(request.Concurrency))
		defer waiting.Release()
		// A counter error is observed but still permits slot acquisition; only a
		// confirmed full queue rejects admission, and release tracks actual counter
		// ownership.
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
		// Recheck funds after an actual wait because admission data can change while
		// the user slot is unavailable; afterWait keeps RPM handling distinct.
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
		// Complete observed partial service before cancellation or replay decisions;
		// another inference attempt could duplicate work already eligible for
		// billing from this provider.
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
		// Refresh credentials while this attempt can still be replayed; committed
		// output belongs to the current inference and cannot be combined with a
		// repeated attempt.
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
				// Complete a served usage-bearing refreshed attempt before considering
				// later recovery, so observed partial work cannot be inferred again on
				// another selected provider.
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
				// A pending refresh can move selection elsewhere while its credential
				// owner continues; exhausted provider allowance returns the
				// pending-refresh classification to the caller.
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
		// Bind the successful session before recording completion so retained
		// affinity follows the served provider; partial failures skip this
		// successful session binding callback.
		if attemptErr == nil {
			if selected.Bind != nil {
				selected.Bind(ctx, result)
			}
			if selected.Complete != nil {
				selected.Complete(ctx, result)
			}
			return nil
		}
		// Exclude the failed provider before selecting another, spending the
		// distinct provider allowance while output has left this request safe for
		// another inference attempt.
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

// attempt acquires or waits for this selection's provider slot, executes once,
// and captures its observed result. It requires a usable selection, tracker,
// and parent lease; missing wait services return a provider-slot Failure.
//
// An already acquired cleanup is registered immediately. Waiting can bypass
// the queue counter when directed; otherwise counter errors are observed while
// confirmed capacity denial stops acquisition. Leases release on return after
// execution supplies its result. ExecutionResult updates happen only for
// actual executor calls, retaining partial usage and route evidence before
// provider release.
func (u *QoderUseCase) attempt(ctx context.Context, request Request, ports RequestPorts, parent *scheduler.Lease, selected *Selection, output *OutputTracker, execution *ExecutionResult) (upstream.AttemptResult, error) {
	ctx = telemetry.WithRequestProvider(ctx, selected.Snapshot.ID, selected.Snapshot.RequestIDHeader)
	if observer, ok := output.Sink.(ExecutionObserver); ok {
		observer.Selected(selected.Snapshot)
	}
	output.BeginAttempt()
	// Own selected slot cleanup before a later wait or execution can fail; parent
	// cleanup and deferred attempt release converge through the same registered
	// lease.
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
		// Continue after counter failure because queue accounting is best effort; a
		// confirmed full provider queue is the distinct refusal handled before
		// acquiring its slot.
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
		// Register the newly acquired slot before execution; the earlier selection
		// cleanup remains separately leased, and deferred releases both tolerate
		// cleanup through their parent lease.
		attempt = scheduler.NewAttemptLease(parent, nil, release)
		defer attempt.Release()
	}
	if err := ctx.Err(); err != nil {
		return upstream.AttemptResult{}, err
	}
	scheduler.ObserveAdmission(ctx)
	result, err := selected.Executor.Execute(ctx, selected.Input, output)
	if execution != nil {
		execution.Attempts++
		execution.Attempt = result
		execution.Provider = selected.Snapshot
		execution.Plan = selected.Plan
		execution.PlanProvided = selected.Plan.ProviderID != 0
	}
	// Capture the executed result before finishing its provider lease, so outer
	// completion can retain usage and route evidence after slot cleanup has run.
	attempt.Finish(scheduler.AttemptOutcome{Served: result.Served})
	return result, err
}
