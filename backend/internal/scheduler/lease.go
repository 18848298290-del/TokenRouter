package scheduler

import (
	"context"
	"slices"
	"sync"
)

// Request cleanup can race with cancellation and with acquiring another
// resource. A lease accepts callbacks while open, closes before detaching its
// callback list, and immediately cleans up resources offered after closure.
// Already registered callbacks run in reverse ownership order, so nested
// provider resources can be returned before their request resources.
//
// Attempt cleanup also determines whether an idle session remains registered.
// AttemptLease fixes the first reported outcome and then releases its physical
// resources. Completion and cancellation policies are chosen by the execution
// entry point: work that continues collecting usage after client cancellation
// can retain its lease until that collection finishes.

// Release modes let execution entry points choose whether request context
// cancellation triggers cleanup or whether completion performs it.
const (
	ReleaseOnCompletion ReleaseMode = iota // caller completes resource cleanup
	ReleaseOnCancel                        // request cancellation triggers cleanup
)

// ReleaseMode lets the entry point choose cancellation cleanup or release after upstream completion.
type ReleaseMode uint8

// Lease owns acquired request resources and closes further registration before
// cleanup. Registered callbacks run in reverse order, while resources offered
// after closure are released immediately by Own.
// @project-doc docs/architecture/gateway_request_lifecycle.md#account_selection_and_failover
type Lease struct {
	// mu protects closed, resources, and stop while ownership is
	// registered or detached. once serializes Release callbacks, and
	// callbacks execute after mu is unlocked so nested cleanup can acquire
	// other owners.
	mu        sync.Mutex  // protects resource ownership state
	closed    bool        // resource registration has ended
	resources []func()    // owned cleanup callbacks
	stop      func() bool // optional cancellation callback stopper
	once      sync.Once   // serializes first cleanup execution
}

// AttemptOutcome determines whether an idle session is retained. Served includes billable partial results.
type AttemptOutcome struct {
	// Served includes successful and billable partial execution.
	// FinishSession retains that idle registration, while the attempt
	// still releases its physical request resources.
	Served bool
}

// AttemptLease owns one provider attempt's resources; the parent request may try another provider afterward.
type AttemptLease struct {
	// resources owns physical cleanup independently from the logical
	// finish callback. once fixes which outcome reaches finish before
	// releasing resources, including a failure supplied by parent cleanup.
	resources *Lease               // acquired attempt resource owner
	finish    func(AttemptOutcome) // optional session outcome callback
	once      sync.Once            // serializes first cleanup execution
}

// requestLeaseKey identifies the request resource owner in the context.
type requestLeaseKey struct{}

// NewLease registers supplied resources and returns their cleanup owner.
// ReleaseOnCancel subscribes to ctx cancellation; completion mode waits for an
// explicit Release.
//
// Resources are registered before the cancellation callback can run, including
// when ctx is already canceled. Publishing the callback stopper under the
// mutex handles Release racing with that subscription. A nil ctx installs no
// cancellation callback, and callers pass each acquired resource through Own
// before depending on the lease to clean it up.
func NewLease(ctx context.Context, mode ReleaseMode, resources ...func()) *Lease {
	l := &Lease{}
	for _, release := range resources {
		l.Own(release)
	}
	if mode == ReleaseOnCancel && ctx != nil {
		// Register cancellation after initial ownership is complete.
		// An already canceled context can start cleanup immediately,
		// so publishing its stopper also checks whether closure has
		// already won.
		stop := context.AfterFunc(ctx, l.Release)
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			stop()
		} else {
			l.stop = stop
			l.mu.Unlock()
		}
	}
	return l
}

// Own transfers one cleanup callback to the lease and reports whether
// registration stayed open. Nil callbacks succeed without adding a resource.
// Transfer each acquisition once, or supply cleanup that tolerates repeated
// transfers of the same resource.
//
// If closure won the mutex first, Own runs the new cleanup immediately after
// unlocking and returns false. Otherwise the callback joins the list detached
// by Release. Late callbacks can therefore run concurrently with already
// detached cleanup, and callers need callbacks that tolerate the cleanup
// timing and do not recursively invoke this same lease Release.
func (l *Lease) Own(release func()) bool {
	if release == nil {
		return true
	}
	// Closure can race this transfer. Decide under mu whether Release will
	// own the callback, then run late cleanup after unlocking so the
	// detached list remains final.
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		release()
		return false
	}
	l.resources = append(l.resources, release)
	l.mu.Unlock()
	return true
}

// Release closes ownership and runs the registered callbacks once in reverse
// order. Nil leases do nothing, and concurrent Release calls wait for the same
// cleanup execution.
//
// The callback list and cancellation stopper are detached under the mutex
// before callbacks run. Own arriving after closure cleans its resource
// immediately instead of appending to a list already being released. Callbacks
// must not recursively call this Release; a panicking callback interrupts
// later callbacks, and sync.Once does not retry that interrupted cleanup.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.mu.Lock()
		// Close admission before detaching callbacks. A concurrent Own
		// then releases its newly acquired resource itself rather than
		// adding it to the detached list.
		l.closed = true
		resources, stop := l.resources, l.stop
		l.resources, l.stop = nil, nil
		l.mu.Unlock()
		if stop != nil {
			stop()
		}
		for _, resource := range slices.Backward(resources) {
			resource()
		}
	})
}

// WrapRelease returns a lease-managed cleanup function for release and the
// selected cancellation policy. A nil callback returns nil, and callers can
// invoke the wrapped function repeatedly or concurrently.
func WrapRelease(ctx context.Context, mode ReleaseMode, release func()) func() {
	if release == nil {
		return nil
	}
	return NewLease(ctx, mode, release).Release
}

// NewAttemptLease constructs the physical resource owner and optional outcome
// callback for one provider attempt. A non-nil parent owns its Release, so
// parent cleanup completes an unfinished attempt with an unserved outcome.
func NewAttemptLease(parent *Lease, finish func(AttemptOutcome), resources ...func()) *AttemptLease {
	a := &AttemptLease{resources: NewLease(context.Background(), ReleaseOnCompletion, resources...), finish: finish}
	if parent != nil {
		parent.Own(a.Release)
	}
	return a
}

// Finish reports the first attempt outcome and then releases physical
// resources. Repeated or concurrent Finish and Release calls share that first
// completion.
//
// The resource release is deferred before invoking the outcome callback, so
// cleanup still runs if that callback panics. Parent cleanup can win with an
// unserved outcome when explicit completion has not begun; successful or
// billable partial execution must call Finish with Served before releasing its
// parent to retain idle session registration.
func (a *AttemptLease) Finish(outcome AttemptOutcome) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		// Install physical cleanup before reporting the logical
		// outcome. A finish callback panic still returns those
		// resources, while once keeps that first outcome attempt
		// final.
		defer a.resources.Release()
		if a.finish != nil {
			a.finish(outcome)
		}
	})
}

// Release finishes an unfinished attempt with an unserved outcome and returns
// resources. A previously finished attempt retains its first outcome, so
// parent cleanup cannot replace a reported successful or billable partial
// completion.
func (a *AttemptLease) Release() { a.Finish(AttemptOutcome{}) }

// WithRequestLease attaches the request resource owner to a non-nil ctx for
// nested selections. It shares the lease pointer; execution entry points still
// determine cancellation policy and when the request completes.
func WithRequestLease(ctx context.Context, lease *Lease) context.Context {
	return context.WithValue(ctx, requestLeaseKey{}, lease)
}

// RequestLease returns the resource owner attached to ctx or nil when absent.
// Nil contexts and differently typed values also return nil, allowing callers
// to operate without request ownership integration.
func RequestLease(ctx context.Context) *Lease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(requestLeaseKey{}).(*Lease)
	return lease
}

// ownRequestResource registers release with the optional request owner carried
// by ctx. Without an owner it does nothing, so the caller retains cleanup
// responsibility and supplies callbacks safe for any earlier attempt cleanup.
func ownRequestResource(ctx context.Context, release func()) {
	if owner := RequestLease(ctx); owner != nil {
		owner.Own(release)
	}
}
