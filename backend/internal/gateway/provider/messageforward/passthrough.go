package messageforward

import (
	"context"
	"net/http"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	claude "github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// passthrough.go executes Anthropic API Key passthrough with request-local
// Messages hooks.
// API Key passthrough retains the Anthropic client response shape while using
// the Messages attempt orchestration. The adapter resolves the provider proxy
// and passes wire, response, and stream observations through hooks belonging to
// the same request.

// anthropicPassthroughAdapter binds native API key output to shared attempt
// preparation.
type anthropicPassthroughAdapter struct {
	// attempt is retained by this synchronous wrapper so credentials,
	// preparation state, and response hooks describe the same selected
	// provider call.
	*attempt // shared Messages preparation state
}

// TokenKind returns the credential kind retained by the passthrough attempt.
func (a *anthropicPassthroughAdapter) TokenKind() string { return a.tokenType }

// ResolveProxy stores the selected provider proxy when both proxy references
// exist. Passthrough uses this direct selection for the exchange, independently
// of the ordinary custom-base relay branch.
func (a *anthropicPassthroughAdapter) ResolveProxy() {
	if a.provider.Record.ProxyID != nil && a.provider.Record.Proxy != nil {
		a.proxyURL = a.provider.Record.Proxy.URL()
	}
}

// MarkPassthrough delegates native output observation to the current HTTP
// request.
func (a *anthropicPassthroughAdapter) MarkPassthrough() {
	a.c.MarkPassthrough()
}

// ExecutePassthrough runs the Anthropic executor using passthrough request and
// response options. The hooks observe this attempt synchronously, and the
// executor closes the successful exchange response before returning.
func (a *anthropicPassthroughAdapter) ExecutePassthrough(ctx context.Context, in *forwardcore.APIKeyInput, h forwardcore.MessageHooks) (upstream.AttemptResult, error) {
	options := a.s.passthroughExchange(a.c, a.state, a.provider, a.token, a.proxyURL, in)
	target := &claude.Target{
		ProviderID: a.provider.Record.ID, Model: in.RequestModel, Passthrough: true, Exchange: options,
		Response: a.s.responseOptions(ctx, a.c, a.state, a.provider, in.RequestModel, true), StartedAt: in.StartTime,
		BeforeResponse: func(ctx context.Context, resp *http.Response, wire []byte) (bool, error) {
			a.response = resp
			return h.Before(ctx, &forwardcore.ExchangeResponse{StatusCode: resp.StatusCode, Headers: resp.Header, RequestID: resp.Header.Get("x-request-id")}, wire)
		},
		OnWireBody: h.Wire,
		OnStream: func(result *claude.StreamResult, _ error) {
			if result != nil {
				h.Stream(&forwardcore.StreamOutcome{Usage: result.Usage, FirstTokenMs: result.FirstTokenMs, ClientDisconnect: result.ClientDisconnect})
			}
		},
	}
	return (claude.Executor{}).Execute(ctx, upstream.AttemptInput{
		Protocol:      protocolcore.ProtocolAnthropicMessages,
		Body:          in.Body,
		ResponseModel: in.OriginalModel,
		Stream:        in.RequestStream,
		Target:        target,
	}, a.c.Sink())
}

// passthrough builds fresh attempt state and runs APIKeyPassthrough for the
// selected provider. The shared flow validates credentials and controls
// preparation, exchange, output, and partial-result handling for this call.
func (r *Runtime) passthrough(ctx context.Context, output HTTPBoundary, target *gatewayprovider.ExecutionProvider, input forwardcore.APIKeyInput) (*forwardcore.Result, error) {
	adapter := &anthropicPassthroughAdapter{attempt: newAttempt(r, output, target)}
	return forwardcore.APIKeyPassthrough(ctx, adapter, adapter.input(), input)
}
