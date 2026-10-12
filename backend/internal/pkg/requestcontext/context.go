package requestcontext

import "context"

// An upstream operation can outlive a disconnected HTTP client while still
// needing a service-controlled stop signal. WithAbort stores a separate server
// signal in request values, and Detach discards the parent's deadline and
// cancellation while reusing those values. Parent cancellation therefore ends
// the request context without ending detached work.
//
// The returned abort function cancels both contexts, and AfterAbort registers
// callbacks against the same stored signal. Detached wrappers obtain Done and
// Err from that signal while their embedded context supplies values and an
// absent deadline. Callers own the abort function and decide when server-side
// work should stop; repeated detachment retains that signal.

// abortKey identifies the server-controlled cancellation signal carried in
// context values.
type abortKey struct{}

// detachedContext combines uncancelled request values with the stored server
// cancellation signal.
type detachedContext struct {
	// Context provides request values after Detach removes the parent deadline;
	// signal supplies Done and Err. Both references are reused by detached copies.
	context.Context                 // retained request value lookup
	signal          context.Context // server-controlled cancellation source
}

// WithAbort returns a child request context and a server-controlled abort
// function. The child observes the parent's cancellation and deadlines, while
// the stored signal is created from Background and remains live when that
// parent ends.
//
// Detach uses the stored signal to let upstream work survive client
// disconnection. The caller invokes abort when server-owned work should end and
// when releasing these cancellation resources. Abort cancels the server signal
// before the request child so callbacks and detached users observe the same
// server stop.
func WithAbort(ctx context.Context) (context.Context, context.CancelFunc) {
	signal, abort := context.WithCancel(context.Background())
	request, cancel := context.WithCancel(context.WithValue(ctx, abortKey{}, signal))
	return request, func() {
		// Publish the server stop first so detached operations and abort callbacks
		// can observe it before the request child is cancelled through this function.
		abort()
		cancel()
	}
}

// AfterAbort schedules callback asynchronously when the stored server signal is
// cancelled. Its returned stop function prevents a callback that has not
// started, following context.AfterFunc; callers handle synchronization with
// callbacks already running. A context without that signal returns a successful
// no-op stopper.
func AfterAbort(ctx context.Context, callback func()) func() bool {
	signal, ok := ctx.Value(abortKey{}).(context.Context)
	if !ok {
		// No server signal means no callback source. Return a successful stopper so
		// callers can use the same cleanup path for contexts with either origin.
		return func() bool { return true }
	}
	return context.AfterFunc(signal, callback)
}

// Detach returns a context that retains request values and any stored server
// signal. Nil input produces Background, and a context without that signal
// produces a value-bearing context whose cancellation and deadline remain
// absent.
//
// For a signalled context, the wrapper obtains Done and Err from the server
// signal while values come from context.WithoutCancel. This lets detached
// forwarding survive parent cancellation but stop after the caller invokes
// WithAbort's abort function. Repeated calls retain the same signal through
// values.
func Detach(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	base := context.WithoutCancel(ctx)
	signal, ok := ctx.Value(abortKey{}).(context.Context)
	if !ok {
		return base
	}
	return detachedContext{Context: base, signal: signal}
}

// Done returns the channel closed when the stored server signal is cancelled.
func (c detachedContext) Done() <-chan struct{} { return c.signal.Done() }

// Err returns the cancellation error currently reported by the stored server
// signal.
func (c detachedContext) Err() error { return c.signal.Err() }
