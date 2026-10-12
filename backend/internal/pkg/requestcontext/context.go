package requestcontext

import "context"

// abortKey stores the server-abort signal created by WithAbort.
type abortKey struct{}

// detachedContext takes cancellation from the server signal and values from Context.
type detachedContext struct {
	context.Context
	signal context.Context
}

// WithAbort returns a request context and an abort function that also cancels detached work.
// The caller must invoke abort when that work should end.
func WithAbort(ctx context.Context) (context.Context, context.CancelFunc) {
	signal, abort := context.WithCancel(context.Background())
	request, cancel := context.WithCancel(context.WithValue(ctx, abortKey{}, signal))
	return request, func() {
		abort()
		cancel()
	}
}

// AfterAbort runs callback asynchronously on server abort and returns a function to stop it.
func AfterAbort(ctx context.Context, callback func()) func() bool {
	signal, ok := ctx.Value(abortKey{}).(context.Context)
	if !ok {
		return func() bool { return true }
	}
	return context.AfterFunc(signal, callback)
}

// Detach retains request values and the server-abort signal while removing parent
// cancellation and deadlines. Without an abort signal, the result never cancels.
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

func (c detachedContext) Done() <-chan struct{} { return c.signal.Done() }
func (c detachedContext) Err() error            { return c.signal.Err() }
