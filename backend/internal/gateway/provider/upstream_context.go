package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
)

// upstream_context.go detaches upstream execution from client cancellation
// when continued response reading is needed.
// Streaming readers may need final usage after a client disconnects, while
// shutdown still needs to stop server-owned work. These adapters delegate to
// requestcontext.Detach to retain request values and its separate abort
// signal, and expose a uniform no-op cleanup handle.

// DetachStreamUpstreamContext detaches streaming requests from client
// cancellation and deadlines while retaining request values and any
// server-abort signal. Nonstreaming contexts pass through; nil becomes Background,
// and the returned cancel function performs no cleanup.
func DetachStreamUpstreamContext(ctx context.Context, stream bool) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	if !stream {
		return ctx, func() {}
	}
	return requestcontext.Detach(ctx), func() {}
}

// DetachUpstreamContext retains request values and any server-abort signal
// while removing client cancellation and deadlines. Nil input returns
// Background, and the returned cancel function is a no-op because
// requestcontext owns the signal.
func DetachUpstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	return requestcontext.Detach(ctx), func() {}
}
