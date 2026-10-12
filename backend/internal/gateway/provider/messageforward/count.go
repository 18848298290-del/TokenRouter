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

// countAttempt holds the counting request and shares request preparation with attempt.
type countAttempt struct {
	*attempt
	request    *http.Request
	proxyReady bool
}

func (a *countAttempt) ResolveModel(ctx context.Context, model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).UpstreamModel(ctx, model)
}

func (a *countAttempt) IsCountClaudeCode(ctx context.Context, metadata string) bool {
	return requeststate.IsClaudeCodeClient(ctx) || clientmeta.IsClaudeCodeClient(a.c.RequestHeaders().Get("User-Agent"), metadata)
}
func (a *countAttempt) TokenKind() string { return a.tokenType }
func (a *countAttempt) BuildCount(ctx context.Context, body []byte, model string, mimic, passthrough bool) ([]byte, error) {
	request, wire, err := a.s.buildCountRequest(ctx, a.c, a.state, a.provider, body, a.token, a.tokenType, model, mimic, passthrough)
	a.request = request
	return wire, err
}

func (a *countAttempt) SendCount(ctx context.Context, passthrough bool) (*forwardcore.ExchangeResponse, error) {
	// Resolve the proxy before the first send. Signature retries reuse it and reload the TLS profile.
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

func (a *countAttempt) ReadCount() ([]byte, error) {
	body, err := a.c.ReadResponseBody(a.response.Body, a.s.options.ResponseReadLimit, CountBody)
	_ = a.response.Body.Close()
	return body, err
}

func (a *countAttempt) IsTooLarge(err error) bool {
	return errors.Is(err, httpclient.ErrResponseBodyTooLarge)
}

func (a *countAttempt) RectifyCount(ctx context.Context, body []byte, model string) bool {
	return a.s.shouldRectify(ctx, a.provider, body, model)
}

func (a *countAttempt) FilterCountRetry(body []byte, model string) []byte {
	return gatewayprovider.FilterThinkingBlocksForRetry(body, model)
}

func (a *countAttempt) CountError(status int, kind, message string) {
	a.c.CountError(status, kind, message)
}

func (a *countAttempt) CountSuccess(status int, headers map[string][]string, body []byte, passthrough bool) {
	a.c.CountSuccess(status, headers, body, passthrough)
}

func (a *countAttempt) SetError(status int, message, detail string) {
	a.c.SetError(status, message, detail)
}

func (a *countAttempt) UnsupportedCount(status int, body []byte) bool {
	return isCountTokensUnsupported404(status, body)
}

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

func (a *countAttempt) CountFailover(status int, headers map[string][]string, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, ResponseHeaders: http.Header(headers).Clone(), RetryableOnSameProvider: retry}
}

func (a *countAttempt) CountURL() string {
	return logredact.SafeUpstreamURL(a.request.URL.String())
}
