package messageforward

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Messages response readers use the current attempt tool map and query cache
// classification as events arrive. Ordinary and passthrough output share stream
// observations, but passthrough alone can request downstream cache-billing
// presentation through the request context.

// These limits bound the Anthropic exchange retry count and total elapsed time
// before final health and client-error handling receives the last response.
const (
	maxRetryAttempts = 5
	maxRetryElapsed  = 10 * time.Second
)

// streamOptions binds the attempt tool map, provider observations, and current
// stream settings for Anthropic readers. Header copying is installed when the
// HTTP adapter has a filter; timeout observation requires health services.
func (r *Runtime) streamOptions(output HTTPBoundary, state *AttemptState, target *gatewayadapter.ExecutionProvider) anthropic.StreamOptions {
	options := anthropic.StreamOptions{
		ProviderID: target.Record.ID,
		ToolNames:  state.ToolNames,
		UpdateWindow: func(ctx context.Context, headers http.Header) {
			gatewayadapter.ObserveExecutionSessionWindow(ctx, r.dependencies.Health, target, headers)
		},
		OverrideCache: func(ctx context.Context) (string, bool) {
			return r.cacheUsageOverride(ctx, target)
		},
		Failover: func(body []byte) error {
			return &forward.UpstreamFailoverError{StatusCode: 502, ResponseBody: body, RetryableOnSameProvider: true}
		},
	}
	if output.RequestPresent() {
		options.UserAgent = output.RequestHeaders().Get("User-Agent")
	}
	options.MaxLineSize = r.options.MaxLineSize
	if r.options.StreamInterval > 0 {
		options.Interval = r.options.StreamInterval
	}
	if r.options.StreamKeepalive > 0 {
		options.Keepalive = r.options.StreamKeepalive
	}
	if output.HasHeaderFilter() {
		options.WriteHeaders = func(dst, src http.Header) { output.WriteHeaders(dst, src, false) }
	}
	if r.dependencies.Health != nil {
		options.OnTimeout = func(ctx context.Context, model string) {
			r.dependencies.Health.Core.HandleStreamTimeout(ctx, gatewayadapter.ExecutionRecord(target), model)
		}
	}
	return options
}

// responseOptions builds bounded body reads and header handling for this
// response mode. Passthrough can request cache-billing presentation from
// context, and invalid JSON is classified with the mapped model on ordinary
// output.
func (r *Runtime) responseOptions(ctx context.Context, output HTTPBoundary, state *AttemptState, target *gatewayadapter.ExecutionProvider, model string, passthrough bool) anthropic.ResponseOptions {
	options := anthropic.ResponseOptions{
		StreamOptions: r.streamOptions(output, state, target),
		ReadBody: func(reader io.Reader) ([]byte, error) {
			return output.ReadResponseBody(reader, r.options.ResponseReadLimit, MessagesBody)
		},
		PreserveContentType: r.options.PreserveContentType,
		ForceCacheBilling:   passthrough && requeststate.IsForceCacheBilling(ctx),
	}
	options.InvalidJSON = func(ctx context.Context, response *http.Response, body []byte, err error) error {
		var models []string
		if !passthrough {
			models = []string{model}
		}
		return gatewayadapter.NonJSONUpstreamFailure(ctx, r.dependencies.Health, response, target, body, err, models...)
	}
	options.WriteHeaders = func(dst, src http.Header) { output.WriteHeaders(dst, src, passthrough) }
	if passthrough && r.dependencies.Health == nil {
		options.UpdateWindow = nil
	}
	return options
}

// readErrorBody returns diagnostic bytes up to the configured error-read cap
// and any reading error. Nil response bodies return nil; callers own closing or
// rebuilding the consumed body.
func (r *Runtime) readErrorBody(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, nil
	}
	// Diagnostic reads default to at most 512 KiB.
	limit := int64(512 << 10)
	if r.options.LogErrorBody && r.options.LogErrorBodyMaxBytes > int(limit) {
		limit = int64(r.options.LogErrorBodyMaxBytes)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}

// detachedStreamContext supplies request values and server abort to streaming
// request construction while dropping client cancellation and deadlines.
// Nonstreaming calls reuse ctx, nil selects Background, and release is an empty
// operation.
func detachedStreamContext(ctx context.Context, stream bool) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	if !stream {
		return ctx, func() {}
	}
	return requestcontext.Detach(ctx), func() {}
}
