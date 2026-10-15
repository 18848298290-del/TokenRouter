package openai

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// http_exchange.go executes HTTP requests while arbitrating header arrival
// against the first-output deadline.
// A first-output budget starts at the supplied request arrival time and
// initially guards the HTTP header wait. If the timer wins, the guarded request
// is cancelled and any returned response body is closed before HeaderTimeout
// selects the caller-visible failure.
//
// Header arrival stops the timer, and a stopped guard remains attached to the
// response body until the caller closes it. That lifetime keeps the guarded
// request context available during response reads. Cleanup uses sync.Once so
// cancellation and context release happen once across error and close paths;
// the body wrapper separately makes its underlying close and recorded close
// error repeatable for callers.

// HTTPExchangeOptions supplies request construction and observations for one
// guarded HTTP exchange.
type HTTPExchangeOptions struct {
	StartedAt time.Time // request arrival timestamp
	// FirstOutputTimeout enables header guarding when positive and is measured from
	// StartedAt; nonpositive values use the unguarded request-context release path.
	FirstOutputTimeout time.Duration
	// RequestContext returns the upstream context and release callback; unguarded
	// construction releases immediately, while guarded exchanges retain release
	// until failure or body closure.
	RequestContext func(context.Context) (context.Context, context.CancelFunc)
	Build          func(context.Context, []byte) (*http.Request, error) // required authenticated request builder
	ApplyHeaders   func(http.Header)                                    // required final header writer
	Do             func(*http.Request) (*http.Response, error)          // required HTTP exchange callback
	Latency        func(time.Duration)                                  // required header-wait latency observer
	HeaderTimeout  func() error                                         // required header timeout translator
	TransportError func(error) error                                    // required transport error translator
}

// openAIFirstOutputHeaderGuard resolves header arrival against the first-output
// cancellation timer.
type openAIFirstOutputHeaderGuard struct {
	// timer publishes expiry through fired and invokes cancel. once controls
	// cleanup of timer, cancel, and release across failed exchanges and body closure.
	cancel  context.CancelFunc // guarded request cancellation callback
	release context.CancelFunc // upstream context release callback
	timer   *time.Timer        // header-wait expiry timer
	// fired closes before timer-driven cancellation; stopHeaderWait waits for that
	// publication when the callback wins the race against header arrival.
	fired chan struct{}
	once  sync.Once // idempotent cleanup publication guard
}

// openAIRequestContextReadCloser ties guarded context cleanup to response-body
// closure.
type openAIRequestContextReadCloser struct {
	io.ReadCloser // original HTTP response reader
	// cleanup cancels and releases the guarded request context; Close invokes it
	// once before closing the underlying response body.
	cleanup func()
	once    sync.Once // idempotent cleanup publication guard
	err     error     // recorded first close error
}

// ExchangeHTTP builds and sends a request with an optional deadline for
// receiving headers. All option callbacks are required when their path is used,
// and StartedAt determines the positive FirstOutputTimeout deadline. The caller
// closes a returned response body to release guarded context ownership.
//
// When timer stopping reports that the deadline callback ran, HeaderTimeout
// wins over the transport result and any returned body is closed. Other
// transport errors use TransportError after cleanup. Successful header arrival
// stops the timer but retains guard cleanup in the returned body wrapper,
// keeping response reads alive until the caller finishes with the body.
func ExchangeHTTP(ctx context.Context, body []byte, options HTTPExchangeOptions) (*http.Response, error) {
	upstreamCtx, release := options.RequestContext(ctx)
	var guard *openAIFirstOutputHeaderGuard
	if options.FirstOutputTimeout > 0 {
		upstreamCtx, guard = newOpenAIFirstOutputHeaderGuard(upstreamCtx, release, options.StartedAt.Add(options.FirstOutputTimeout))
	}
	request, err := options.Build(upstreamCtx, body)
	if guard == nil {
		release()
	}
	if err != nil {
		if guard != nil {
			guard.close()
		}
		return nil, err
	}
	options.ApplyHeaders(request.Header)
	started := time.Now()
	response, err := options.Do(request)
	options.Latency(time.Since(started))
	if guard != nil && guard.stopHeaderWait() {
		// A transport can return a body while cleanup is required. Close it before
		// returning failure so an unclaimed connection cannot remain acquired by this exchange.
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		guard.close()
		return nil, options.HeaderTimeout()
	}
	if err != nil {
		// A transport can return a body while cleanup is required. Close it before
		// returning failure so an unclaimed connection cannot remain acquired by this exchange.
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if guard != nil {
			guard.close()
		}
		return nil, options.TransportError(err)
	}
	if guard != nil {
		response.Body = &openAIRequestContextReadCloser{ReadCloser: response.Body, cleanup: guard.close}
	}
	return response, nil
}

// newOpenAIFirstOutputHeaderGuard returns a cancellable context and timer
// owning release. An expired deadline schedules the callback with a
// one-nanosecond delay; callers stop header waiting and eventually close the
// guard on every exit.
func newOpenAIFirstOutputHeaderGuard(
	ctx context.Context,
	release context.CancelFunc,
	deadline time.Time,
) (context.Context, *openAIFirstOutputHeaderGuard) {
	guardedCtx, cancel := context.WithCancel(ctx)
	guard := &openAIFirstOutputHeaderGuard{cancel: cancel, release: release, fired: make(chan struct{})}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		remaining = time.Nanosecond
	}
	// Publish expiry before cancelling transport so stopHeaderWait can choose
	// the timeout result while the request is waking from cancellation.
	guard.timer = time.AfterFunc(remaining, func() {
		close(guard.fired)
		cancel()
	})
	return guardedCtx, guard
}

// stopHeaderWait stops the header deadline or waits for its callback to publish
// expiry. It returns false only when timer stopping succeeds, letting
// ExchangeHTTP accept the transport result. A false Stop result requires
// waiting on fired before reporting timeout.
//
// The callback closes fired before cancelling the request context, so
// header-wait completion observes that the timer already won even if transport
// returned concurrently. Callers invoke this operation once during the header
// phase and use close for cleanup afterward; repeated stop calls after a
// successfully stopped timer would wait indefinitely.
func (g *openAIFirstOutputHeaderGuard) stopHeaderWait() bool {
	if g.timer.Stop() {
		return false
	}
	// Stop returning false means the callback has started or finished; wait for
	// its expiry publication before the header path decides which result wins.
	<-g.fired
	return true
}

// close stops the timer and invokes guarded cancellation and release once.
// Callers use it on failed construction, transport failure, timeout, or body
// closure; repeated cleanup calls have no further effect.
func (g *openAIFirstOutputHeaderGuard) close() {
	g.once.Do(func() {
		// Cleanup can run from error handling or response closure; publish all
		// cancellation and release effects once so shared context bookkeeping is not repeated.
		g.timer.Stop()
		g.cancel()
		g.release()
	})
}

// Close cancels the guarded context before closing the underlying response
// body. The first invocation stores its close error and runs both operations
// once; subsequent calls return that recorded error.
func (r *openAIRequestContextReadCloser) Close() error {
	r.once.Do(func() {
		r.cleanup()
		r.err = r.ReadCloser.Close()
	})
	return r.err
}
