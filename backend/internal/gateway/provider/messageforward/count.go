package messageforward

import (
	"context"
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Count Tokens shares Messages credential and model preparation while retaining
// its own request and response presentation. Proxy selection is remembered
// after the first send, and each send resolves the TLS profile again for the
// selected provider.

// countAttempt retains counting exchange state alongside its Messages
// preparation adapter.
type countAttempt struct {
	// attempt shares counting credentials and preparation state with this
	// wrapper, while request and proxyReady track the counting-specific
	// send sequence.
	*attempt // shared Messages preparation state
	// request is replaced by BuildCount and read by SendCount; callers
	// build successfully before sending this attempt.
	request *http.Request // prepared counting HTTP request
	// proxyReady keeps the first selected proxy across counting retries
	// while SendCount refreshes the provider TLS profile each time.
	proxyReady bool // first proxy selection marker
}

// ResolveModel returns the selected provider and platform model for token
// counting.
func (a *countAttempt) ResolveModel(ctx context.Context, model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).UpstreamModel(ctx, model)
}

// IsCountClaudeCode recognizes counting clients through context, user agent, or
// metadata.
func (a *countAttempt) IsCountClaudeCode(ctx context.Context, metadata string) bool {
	return requeststate.IsClaudeCodeClient(ctx) || clientmeta.IsClaudeCodeClient(a.c.RequestHeaders().Get("User-Agent"), metadata)
}

// TokenKind returns the credential kind saved for this counting attempt.
func (a *countAttempt) TokenKind() string { return a.tokenType }

// BuildCount stores a counting request and returns its prepared wire bytes.
// Passthrough may return nil wire bytes, and callers check build errors before
// invoking SendCount.
func (a *countAttempt) BuildCount(ctx context.Context, body []byte, model string, mimic, passthrough bool) ([]byte, error) {
	request, wire, err := a.s.buildCountRequest(ctx, a.c, a.state, a.provider, body, a.token, a.tokenType, model, mimic, passthrough)
	a.request = request
	return wire, err
}

// SendCount sends the request built by BuildCount and retains its successful
// response. Proxy choice is fixed on the first send, TLS is resolved on each
// send, and request construction supplies cancellation.
func (a *countAttempt) SendCount(ctx context.Context, passthrough bool) (*forwardcore.ExchangeResponse, error) {
	// Resolve the proxy before the first send. Signature retries reuse it
	// and reload the TLS profile.
	if !a.proxyReady {
		a.proxyReady = true
		if a.provider.Record.ProxyID != nil && a.provider.Record.Proxy != nil && (passthrough || !a.provider.View().IsCustomBaseURLEnabled() || a.provider.View().GetCustomBaseURL() == "") {
			a.proxyURL = a.provider.Record.Proxy.URL()
		}
	}
	resp, err := a.s.dependencies.Transport.DoWithTLS(a.request, a.proxyURL, a.provider.Record.ID, a.provider.Record.Concurrency, a.s.requestTLS(a.provider))
	if err != nil {
		return nil, err
	}
	a.response = resp
	return &forwardcore.ExchangeResponse{StatusCode: resp.StatusCode, Headers: resp.Header, RequestID: resp.Header.Get("x-request-id")}, nil
}

// ReadCount reads the retained response through the counting body limit and
// closes it afterward. Callers invoke it after a successful send and propagate
// reading or oversize presentation errors.
func (a *countAttempt) ReadCount() ([]byte, error) {
	body, err := a.c.ReadResponseBody(a.response.Body, a.s.options.ResponseReadLimit, CountBody)
	_ = a.response.Body.Close()
	return body, err
}

// IsTooLarge reports whether the error chain contains a response-size limit
// failure.
func (a *countAttempt) IsTooLarge(err error) bool {
	return errors.Is(err, httpclient.ErrResponseBodyTooLarge)
}

// RectifyCount reports whether current policy permits this counting signature
// recovery.
func (a *countAttempt) RectifyCount(ctx context.Context, body []byte, model string) bool {
	return a.s.shouldRectify(ctx, a.provider, body, model)
}

// FilterCountRetry delegates model-specific thinking cleanup for a counting
// retry body.
func (a *countAttempt) FilterCountRetry(body []byte, model string) []byte {
	return gatewayprovider.FilterThinkingBlocksForRetry(body, model)
}

// CountError delegates the supplied counting failure to the current HTTP
// adapter.
func (a *countAttempt) CountError(status int, kind, message string) {
	a.c.CountError(status, kind, message)
}

// CountSuccess delegates upstream counting bytes and headers using the selected
// passthrough mode.
func (a *countAttempt) CountSuccess(status int, headers map[string][]string, body []byte, passthrough bool) {
	a.c.CountSuccess(status, headers, body, passthrough)
}

// SetError stores counting failure details in the current HTTP observation
// state.
func (a *countAttempt) SetError(status int, message, detail string) {
	a.c.SetError(status, message, detail)
}

// UnsupportedCount recognizes a missing counting route from its status and
// body.
func (a *countAttempt) UnsupportedCount(status int, body []byte) bool {
	return isCountTokensUnsupported404(status, body)
}

// CountHealth combines the provider error policy with optional persisted health
// observation. Status defaults remain available when Health is absent, and the
// result carries generic, failover, and same-provider retry choices.
func (a *countAttempt) CountHealth(ctx context.Context, status int, headers map[string][]string, body []byte, model string) forwardcore.ErrorDecision {
	d := providercore.ErrorDecisionWithoutPersistence(gatewayprovider.ExecutionErrorPolicy(a.provider), status)
	if a.s.dependencies.Health != nil {
		d = gatewayprovider.ApplyExecutionHealth(ctx, a.s.dependencies.Health, a.provider, gatewayprovider.HealthObservationFromContext(ctx, status, headers, body, []string{model}))
	}
	return forwardcore.ErrorDecision{
		Generic:           d.ShouldReturnGenericError(),
		Failover:          d.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(a.provider), status, forwardcore.ShouldFailover(status)),
		RetrySameProvider: d.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(a.provider), status),
	}
}

// CountFailover returns a counting failover error with cloned response headers.
// Body bytes remain shared with the supplied input, and retry indicates
// eligibility for the outer same-provider loop.
func (a *countAttempt) CountFailover(status int, headers map[string][]string, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: http.Header(headers).Clone(), RetryableOnSameProvider: retry}
}

// CountURL returns the retained request URL sanitized for failure diagnostics.
func (a *countAttempt) CountURL() string {
	return logredact.SafeUpstreamURL(a.request.URL.String())
}
