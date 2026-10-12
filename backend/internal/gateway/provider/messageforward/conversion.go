package messageforward

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// conversionAttempt holds conversion state, credentials, and network handles for one attempt.
type conversionAttempt struct {
	s                          *Runtime
	c                          HTTPBoundary
	provider                   *gatewayprovider.ExecutionProvider
	responses                  bool
	state                      *AttemptState
	token, tokenType, proxyURL string
	request                    *http.Request
	response                   *http.Response
}

func (a *conversionAttempt) NormalizeResponses(body []byte) ([]byte, bool, error) {
	return gatewayprovider.NormalizeOpenAIResponsesLegacyIngress(body)
}

func (a *conversionAttempt) ResolveModel(ctx context.Context, model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).UpstreamModel(ctx, model)
}

func (a *conversionAttempt) Effort(body []byte, chat bool, models ...string) *string {
	return forwardcore.ExtractEffort(body, chat, capability.NormalizeRecordedOpenAIEffortForModel, models...)
}

func (a *conversionAttempt) ThinkingFallback(effort *string, body []byte, model string) *string {
	return gatewayprovider.ApplyThinkingEnabledFallback(effort, body, model)
}

func (a *conversionAttempt) ModelNotice(message, original, mapped string, stream bool) {
	logging.L().Debug(message, zap.Int64("provider_id", a.provider.Record.ID), zap.String("original_model", original), zap.String("mapped_model", mapped), zap.Bool("client_stream", stream))
}

func (a *conversionAttempt) Mimic(ctx context.Context, body []byte, system json.RawMessage, model string) []byte {
	return a.s.mimic(ctx, a.c, a.state, a.provider, body, system, model)
}

func (a *conversionAttempt) CacheLimit(body []byte) []byte {
	return anthropic.EnforceCacheControlLimit(body)
}

func (a *conversionAttempt) Credential(ctx context.Context) error {
	token, kind, err := a.s.dependencies.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(a.provider))
	if err == nil {
		a.token, a.tokenType = token, kind
	}
	return err
}

func (a *conversionAttempt) Build(ctx context.Context, body []byte, model string, stream, mimic bool) ([]byte, error) {
	if a.provider.Record.ProxyID != nil && a.provider.Record.Proxy != nil {
		a.proxyURL = a.provider.Record.Proxy.URL()
	}
	upstreamCtx, release := detachedStreamContext(ctx, stream)
	request, wire, err := a.s.buildRequest(upstreamCtx, a.c, a.state, a.provider, body, a.token, a.tokenType, model, stream, mimic)
	release()
	a.request = request
	return wire, err
}

func (a *conversionAttempt) Send(ctx context.Context) (forwardcore.Response, error) {
	resp, err := a.s.dependencies.Transport.DoWithTLS(a.request, a.proxyURL, a.provider.Record.ID, a.provider.Record.Concurrency, a.s.requestTLS(a.provider))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return forwardcore.Response{}, a.s.transportError(ctx, a.c, a.provider, err, forwardcore.Notice{UpstreamURL: logredact.SafeUpstreamURL(a.request.URL.String())})
	}
	a.response = resp
	return a.s.forwardResponse(resp), nil
}

func (a *conversionAttempt) ReadErrorBody() ([]byte, error) {
	body, err := a.s.readErrorBody(a.response)
	_ = a.response.Body.Close()
	a.response.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}

func (a *conversionAttempt) ErrorMessage(body []byte) string {
	return logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
}

func (a *conversionAttempt) Health(ctx context.Context, status int, body []byte, model string) forwardcore.ErrorDecision {
	decision := providercore.ErrorDecisionWithoutPersistence(gatewayprovider.ExecutionErrorPolicy(a.provider), status)
	if a.s.dependencies.Health != nil {
		decision = gatewayprovider.ApplyExecutionHealth(ctx, a.s.dependencies.Health, a.provider, gatewayprovider.HealthObservationFromContext(ctx, status, a.response.Header, body, []string{model}))
	}
	return forwardcore.ErrorDecision{
		Generic:           decision.ShouldReturnGenericError(),
		Failover:          decision.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(a.provider), status, forwardcore.ShouldFailover(status)),
		RetrySameProvider: decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(a.provider), status),
	}
}

func (a *conversionAttempt) FailoverNotice(status int, message string) {
	a.c.Observe(forwardcore.Notice{
		Platform: a.provider.Record.Platform, ProviderID: a.provider.Record.ID, ProviderName: a.provider.Record.Name, UpstreamStatusCode: status, UpstreamRequestID: a.response.Header.Get("x-request-id"), Kind: "failover", Message: message,
	})
}

func (a *conversionAttempt) FailoverError(status int, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, RetryableOnSameProvider: retry}
}

func (a *conversionAttempt) Output() forwardcore.Output {
	return a.c.ConversionOutput(a.responses, a.state)
}

// forwardResponse creates a scanner with a 64 KiB initial buffer and the configured line limit.
func (s *Runtime) forwardResponse(resp *http.Response) forwardcore.Response {
	maxLineSize := 500 * 1024 * 1024
	if s.options.MaxLineSize > 0 {
		maxLineSize = s.options.MaxLineSize
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	return forwardcore.Response{
		StatusCode:       resp.StatusCode,
		Close:            func() { _ = resp.Body.Close() },
		Runtime:          bridge.Runtime{Now: time.Now, ReadRandom: rand.Read},
		RequestID:        resp.Header.Get("x-request-id"),
		Headers:          resp.Header,
		Lines:            scanner,
		MaxSSEFrameBytes: maxLineSize,
	}
}
