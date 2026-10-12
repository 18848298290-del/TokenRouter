package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
)

// DetachStreamUpstreamContext detaches streaming requests from client cancellation
// and deadlines while retaining the server-abort signal. Nonstreaming contexts
// pass through; nil becomes Background. The returned cancel function is a no-op.
func DetachStreamUpstreamContext(ctx context.Context, stream bool) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	if !stream {
		return ctx, func() {}
	}
	return requestcontext.Detach(ctx), func() {}
}

// DetachUpstreamContext retains request values and the server-abort signal while
// removing client cancellation and deadlines. Nil becomes Background, and the
// returned cancel function is a no-op.
func DetachUpstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	return requestcontext.Detach(ctx), func() {}
}
