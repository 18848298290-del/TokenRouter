package payment

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// expiry.go schedules ordered payment reconciliation and expiration rounds and
// coordinates their startup and shutdown.
// Late callbacks, provider processing, and unfinished grants require repeated
// recovery while payment orders can also expire. OrderExpiry runs those four
// phases in a fixed order, immediately after startup and on later interval
// ticks, with optional leader admission before a round.
//
// Each phase receives a separate thirty-second child of the running context;
// application signal cancellation or an earlier caller deadline can shorten
// it. Phase errors are observed while the live parent permits later phases.
// Start and StopContext coordinate permanent admission state under a mutex,
// and a completion channel reports the worker's actual return. Stop
// cancellation therefore differs from completion: an uncooperative phase can
// outlast a caller's waiting budget. The constructor binds dependencies and
// leaves startup to its caller.

const (
	// OrderExpiryLeaderKey and OrderExpiryLeaderTTL configure the shared lease
	// requested by application expiry assembly before running a recovery round.
	// leader lease lookup key.
	OrderExpiryLeaderKey = "payment:order:expiry:leader"
	OrderExpiryLeaderTTL = 3 * time.Minute // leader lease expiration duration
	// orderExpiryStepTimeout bounds each phase separately; parent cancellation or
	// an earlier parent deadline can end that phase before this budget expires.
	orderExpiryStepTimeout = 30 * time.Second
)

// OrderReconciler exposes the ordered recovery phases invoked by payment
// expiry.
type OrderReconciler interface {
	// ReconcilePendingPaymentOrders queries eligible pending payment candidates
	// and counts confirmed paid outcomes. The lifecycle implementation checks
	// context between orders and logs individual failures while returning
	// batch-query or cancellation errors.
	ReconcilePendingPaymentOrders(context.Context) (int, error)
	// ReconcileProcessingOrders queries a selected page of provider-processing
	// orders and counts recovered paid outcomes. Provider failures can expire
	// those orders, while individual query/apply failures are logged and
	// remaining candidates continue.
	ReconcileProcessingOrders(context.Context) (int, error)
	// ReconcilePaidFulfillmentOrders retries a selected page of eligible paid,
	// failed, or stale recharging orders. Its count includes orders reread as
	// completed; individual fulfillment errors leave later rows eligible to run.
	ReconcilePaidFulfillmentOrders(context.Context) (int, error)
	// ExpireTimedOutOrders examines expired pending rows and counts accepted
	// closing outcomes. Provider queries can instead confirm paid or processing
	// state, and uncertain cancellation keeps an order available for later
	// recovery.
	ExpireTimedOutOrders(context.Context) (int, error)
}

// ExpiryRuntime supplies optional round admission and per-phase observation
// callbacks.
type ExpiryRuntime struct {
	// Acquire gates one round and returns an optional release on success. Its
	// acquisition context is cancelled immediately after the call; release
	// remains callable after that cancellation.
	Acquire func(context.Context) (func(), bool)
	// Observe receives each invoked phase's name, count, and error after its
	// child context is cancelled. Its synchronous work runs before the next phase
	// starts.
	Observe func(string, int, error)
}

// OrderExpiry runs payment recovery immediately and periodically until
// cancellation or stop. Construct it with NewOrderExpiry so the completion
// channel is initialized before Start or StopContext.
type OrderExpiry struct {
	service  OrderReconciler // ordered payment recovery operations
	interval time.Duration   // recovery tick interval
	runtime  ExpiryRuntime   // round admission and observation callbacks
	// mu, started, stopped, and cancel coordinate Start and StopContext. State
	// publication and cancellation selection occur under this mutex, while worker
	// completion is reported through done.
	mu      sync.Mutex         // startup and shutdown state mutex
	started bool               // worker admission has occurred
	stopped bool               // permanent stop admission flag
	cancel  context.CancelFunc // admitted worker cancellation function
	// done is created by NewOrderExpiry. The admitted worker closes it on return,
	// or StopContext closes it before startup; waiting callers use that signal to
	// observe termination.
	done chan struct{}
}

// NewOrderExpiry binds the recovery service, interval, and callbacks and
// creates its completion channel. Call Start to run work; missing service or a
// nonpositive interval leaves startup inactive, and StopContext permanently
// closes this instance.
func NewOrderExpiry(service OrderReconciler, interval time.Duration, runtime ExpiryRuntime) *OrderExpiry {
	return &OrderExpiry{service: service, interval: interval, runtime: runtime, done: make(chan struct{})}
}

// Start launches the first expiry worker for an initialized instance with a
// service and positive interval. Nil receiver, an earlier start, or any prior
// StopContext returns immediately. The supplied nonnil context owns
// cancellation and any earlier deadline inherited by recovery phases.
//
// The mutex publishes the cancellation function and started flag before
// launching the worker, so StopContext can cancel the admitted run even when
// the goroutine has not begun executing. The worker runs its first round
// immediately, then uses interval ticks until the running context ends.
func (s *OrderExpiry) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped || s.service == nil || s.interval <= 0 {
		return
	}
	// Publish the cancellation function and started flag under mu before
	// launching the worker. StopContext can then find the admitted run before its
	// immediate recovery round begins.
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	go s.run(runCtx)
}

// run executes one immediate recovery round, then sequential rounds on
// interval ticks until the supplied running context ends. Start invokes it
// once after publishing worker state; each round returns before the next
// selected tick is handled.
//
// Completion closes done after round work returns and the worker exits,
// allowing StopContext to distinguish cancellation from actual termination.
// The ticker is stopped during that return. A dependency ignoring its phase
// context can keep the worker and completion signal pending until that
// dependency returns.
func (s *OrderExpiry) run(ctx context.Context) {
	// Closing done reports worker termination after the active round returns.
	// StopContext cancellation alone cannot claim completion while a phase
	// remains inside its dependency.
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

// StopContext permanently stops admission, cancels an admitted worker, and
// waits for its completion signal. A nil receiver succeeds; stopping before
// Start closes the initialized completion channel directly, and repeated calls
// join the same signal.
//
// The mutex coordinates stopped, started, and cancel so only one path closes a
// worker that never ran. A started worker closes done on return. Already
// observed completion succeeds before inspecting the waiting context;
// otherwise the caller's context can return an unfinished error while recovery
// is still executing. A later call can continue waiting for actual completion.
func (s *OrderExpiry) StopContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.stopped {
		// Mark the instance stopped before choosing running-worker cancellation or
		// pre-start channel closure. Later Start calls cannot admit another worker
		// that would close this signal again.
		s.stopped = true
		if s.started {
			s.cancel()
		} else {
			close(s.done)
		}
	}
	done := s.done
	s.mu.Unlock()
	// Check an already closed completion signal first so a caller with an expired
	// waiting context still receives success for work observed as finished.
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("payment order expiry unfinished: %w", ctx.Err())
	}
}

// runOnce attempts optional leader admission, then executes pending,
// processing, fulfillment, and expiry phases in that order. A cancelled parent
// skips admission or the next phase, and unsuccessful admission skips the
// round. A successful admission keeps its optional release until all invoked
// work has returned.
//
// Lock acquisition has its own two-second child context. Each phase then gets
// a separate thirty-second child of the running context, so an earlier parent
// deadline shortens that phase; there is no round deadline added here. Phase
// errors are observed and later phases can continue while the parent is live.
// Called operations remain responsible for returning when their contexts end.
func (s *OrderExpiry) runOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if s.runtime.Acquire != nil {
		// The short child bounds admission only. Cancel it after Acquire returns,
		// while retaining the successful round's separate release callback until
		// phase work has returned.
		lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		release, ok := s.runtime.Acquire(lockCtx)
		cancel()
		if !ok {
			return
		}
		if release != nil {
			// Keep admission while the invoked phases run. Release after their calls
			// return, including parent cancellation paths that stop between phases.
			defer release()
		}
	}
	steps := []struct {
		// name and run pair an observation label with its phase operation. The loop
		// invokes these pairs in order and reports each returned count and error
		// before advancing.
		name string                             // observed recovery phase name
		run  func(context.Context) (int, error) // recovery phase operation
	}{
		{"pending", s.service.ReconcilePendingPaymentOrders},
		{"processing", s.service.ReconcileProcessingOrders},
		{"fulfillment", s.service.ReconcilePaidFulfillmentOrders},
		{"expire", s.service.ExpireTimedOutOrders},
	}
	for _, step := range steps {
		if ctx.Err() != nil {
			return
		}
		// Give this phase its own budget from the running context, then retire its
		// timer after the call. A prior phase's timeout does not consume this child
		// budget.
		stepCtx, cancel := context.WithTimeout(ctx, orderExpiryStepTimeout)
		n, err := step.run(stepCtx)
		cancel()
		if s.runtime.Observe != nil {
			s.runtime.Observe(step.name, n, err)
		}
	}
}
