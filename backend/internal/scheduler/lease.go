package scheduler

import (
	"context"
	"slices"
	"sync"
)

const (
	ReleaseOnCompletion ReleaseMode = iota
	ReleaseOnCancel
)

// ReleaseMode lets the entry point choose cancellation cleanup or release after upstream completion.
type ReleaseMode uint8

// Lease owns acquired request resources and releases them in reverse acquisition order.
// @project-doc docs/architecture/gateway_request_lifecycle.md#account_selection_and_failover
type Lease struct {
	mu        sync.Mutex
	closed    bool
	resources []func()
	stop      func() bool
	once      sync.Once
}

// AttemptOutcome determines whether an idle session is retained. Served includes billable partial results.
type AttemptOutcome struct {
	Served bool
}

// AttemptLease owns one provider attempt's resources; the parent request may try another provider afterward.
type AttemptLease struct {
	resources *Lease
	finish    func(AttemptOutcome)
	once      sync.Once
}

// requestLeaseKey identifies the request resource owner in the context.
type requestLeaseKey struct{}

// NewLease registers resources before cancellation. ReleaseOnCancel also cleans up when ctx is already canceled.
func NewLease(ctx context.Context, mode ReleaseMode, resources ...func()) *Lease {
	l := &Lease{}
	for _, release := range resources {
		l.Own(release)
	}
	if mode == ReleaseOnCancel && ctx != nil {
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

// Own takes responsibility for releasing an acquired resource. A closed lease releases it immediately and returns false.
func (l *Lease) Own(release func()) bool {
	if release == nil {
		return true
	}
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

// Release cleans up once across cancellation, error, and completion paths. Concurrent calls wait for that cleanup.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.mu.Lock()
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

// WrapRelease returns a lease-managed release function, or nil when release is nil.
func WrapRelease(ctx context.Context, mode ReleaseMode, release func()) func() {
	if release == nil {
		return nil
	}
	return NewLease(ctx, mode, release).Release
}

// NewAttemptLease registers with the parent; an unfinished attempt is cleaned up as a failure.
func NewAttemptLease(parent *Lease, finish func(AttemptOutcome), resources ...func()) *AttemptLease {
	a := &AttemptLease{resources: NewLease(context.Background(), ReleaseOnCompletion, resources...), finish: finish}
	if parent != nil {
		parent.Own(a.Release)
	}
	return a
}

// Finish records the outcome before releasing resources. Parent cleanup and Finish share one execution.
func (a *AttemptLease) Finish(outcome AttemptOutcome) {
	if a == nil {
		return
	}
	a.once.Do(func() {
		defer a.resources.Release()
		if a.finish != nil {
			a.finish(outcome)
		}
	})
}

// Release finishes an unfinished attempt as a failure.
func (a *AttemptLease) Release() { a.Finish(AttemptOutcome{}) }

// WithRequestLease passes the acquired user lease to provider attempts; the execution layer controls release timing.
func WithRequestLease(ctx context.Context, lease *Lease) context.Context {
	return context.WithValue(ctx, requestLeaseKey{}, lease)
}

func RequestLease(ctx context.Context) *Lease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(requestLeaseKey{}).(*Lease)
	return lease
}

// ownRequestResource registers cleanup for request exit; lease-managed callbacks tolerate earlier attempt cleanup.
func ownRequestResource(ctx context.Context, release func()) {
	if owner := RequestLease(ctx); owner != nil {
		owner.Own(release)
	}
}
