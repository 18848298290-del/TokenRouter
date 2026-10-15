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

// conversion.go adapts Anthropic exchanges for Chat and Responses output and
// retains error evidence.
// Chat and Responses conversion consume Anthropic events even for a buffered
// client reply. The adapter retains request-local credentials, tool state, and
// response handles while gateway/forward joins or emits those events using the
// chosen client protocol. Streaming request construction detaches client
// cancellation and retains any server abort carried by requestcontext.
//
// Error inspection consumes a bounded prefix and rebuilds the response reader
// from the captured bytes, including bytes returned with a read error. Later
// health checks can inspect that prefix after the network body has closed. The
// response cleanup callback reads the current body so conversion orchestration
// also closes a replacement installed during error handling.

// conversionAttempt holds exchange handles and state for one OpenAI conversion
// attempt.
type conversionAttempt struct {
	// s, c, and provider are borrowed for one synchronous conversion; their
	// owners keep these references usable through request construction,
	// transport, and client response output.
	//
	// shared Messages forwarding runtime
	s *Runtime
	c HTTPBoundary // current HTTP output adapter
	// selected execution provider state
	provider  *gatewayprovider.ExecutionProvider
	responses bool // Responses presentation mode flag
	// state holds the conversion data created by Chat or Responses; output
	// callbacks borrow its tool mapping until synchronous conversion
	// finishes.
	//
	// current tool restoration state
	state *AttemptState
	// token and tokenType are stored on credential success; proxyURL is
	// selected before Build, and Send reads these saved attempt values.
	//
	// token access credential; tokenType credential kind; proxyURL exchange
	// proxy
	token, tokenType, proxyURL string
	// Build stores request before Send stores response. Conversion
	// orchestration then reads response through its scanner and invokes the
	// returned Close callback after processing.
	//
	// prepared upstream HTTP request
	request *http.Request
	// successful upstream response handle
	response *http.Response
}

// NormalizeResponses delegates supported legacy Responses normalization to the
// provider adapter.
func (a *conversionAttempt) NormalizeResponses(body []byte) ([]byte, bool, error) {
	return gatewayprovider.NormalizeOpenAIResponsesLegacyIngress(body)
}

// ResolveModel returns the selected provider and platform wire model for
// conversion.
func (a *conversionAttempt) ResolveModel(ctx context.Context, model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).UpstreamModel(ctx, model)
}

// Effort extracts recorded OpenAI reasoning effort for the supplied model
// candidates.
func (a *conversionAttempt) Effort(body []byte, chat bool, models ...string) *string {
	return forwardcore.ExtractEffort(body, chat, capability.NormalizeRecordedOpenAIEffortForModel, models...)
}

// ThinkingFallback fills missing recorded effort for eligible thinking-enabled
// converted requests.
func (a *conversionAttempt) ThinkingFallback(effort *string, body []byte, model string) *string {
	return gatewayprovider.ApplyThinkingEnabledFallback(effort, body, model)
}

// ModelNotice logs selected provider, logical model, wire model, and client
// mode.
func (a *conversionAttempt) ModelNotice(message, original, mapped string, stream bool) {
	logging.L().Debug(message, zap.Int64("provider_id", a.provider.Record.ID), zap.String("original_model", original), zap.String("mapped_model", mapped), zap.Bool("client_stream", stream))
}

// Mimic delegates OAuth preparation using the conversion attempt tool
// restoration state.
func (a *conversionAttempt) Mimic(ctx context.Context, body []byte, system json.RawMessage, model string) []byte {
	return a.s.mimic(ctx, a.c, a.state, a.provider, body, system, model)
}

// CacheLimit delegates Anthropic cache breakpoint limit enforcement for
// converted request bytes.
func (a *conversionAttempt) CacheLimit(body []byte) []byte {
	return anthropic.EnforceCacheControlLimit(body)
}

// Credential stores token and kind after successful provider credential
// resolution. Failure leaves the saved values unchanged, and callers stop
// preparation before constructing or sending the conversion request.
func (a *conversionAttempt) Credential(ctx context.Context) error {
	token, kind, err := a.s.dependencies.Credentials.Resolve(ctx, gatewayprovider.ExecutionRecord(a.provider))
	if err == nil {
		a.token, a.tokenType = token, kind
	}
	return err
}

// Build stores the prepared conversion request and returns its wire bytes.
// Streaming construction removes client cancellation while retaining server
// abort; callers check construction errors before sending the saved request.
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

// Send exchanges the saved request and returns scanner, header, and close
// handles after success. Transport failure closes any returned body, records
// diagnostics, and returns cancellation or failover to its caller.
func (a *conversionAttempt) Send(ctx context.Context) (forwardcore.Response, error) {
	resp, err := a.s.dependencies.Transport.DoWithTLS(a.request, a.proxyURL, a.provider.Record.ID, a.provider.Record.Concurrency, a.s.requestTLS(a.provider))
	if err != nil {
		// A failed transport can still return an open body; close it
		// before error presentation because no successful result
		// transfers that handle to conversion.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return forwardcore.Response{}, a.s.transportError(ctx, a.c, a.provider, err, forwardcore.Notice{UpstreamURL: logredact.SafeUpstreamURL(a.request.URL.String())})
	}
	a.response = resp
	return a.s.forwardResponse(resp), nil
}

// ReadErrorBody reads bounded diagnostics and rebuilds the retained response
// body from the captured bytes. Callers invoke it with a response and body from
// a successful Send, and inspect the returned reading error before proceeding.
//
// Closing the network reader precedes replacement so later health inspection
// uses the captured prefix. Bytes returned with a reading error still become
// the replacement body; unread bytes beyond the diagnostic cap are discarded.
// Conversion orchestration owns eventual closure through the saved response
// cleanup callback, which reads the current body.
func (a *conversionAttempt) ReadErrorBody() ([]byte, error) {
	body, err := a.s.readErrorBody(a.response)
	// Close the consumed reader and install captured bytes so subsequent
	// health and diagnostic checks can inspect the same bounded upstream
	// error body.
	_ = a.response.Body.Close()
	a.response.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}

// ErrorMessage returns trimmed upstream error text after sanitizing sensitive
// query values.
func (a *conversionAttempt) ErrorMessage(body []byte) string {
	return logredact.SanitizeUpstreamQueries(strings.TrimSpace(upstream.ExtractErrorMessage(body)))
}

// Health combines provider policy with optional persisted observation for the
// selected model. It reads retained response headers and returns generic,
// failover, and same-provider choices for the conversion flow.
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

// FailoverNotice records provider and upstream response identity in request
// diagnostics.
func (a *conversionAttempt) FailoverNotice(status int, message string) {
	a.c.Observe(forwardcore.Notice{
		Platform: a.provider.Record.Platform, ProviderID: a.provider.Record.ID, ProviderName: a.provider.Record.Name, UpstreamStatusCode: status, UpstreamRequestID: a.response.Header.Get("x-request-id"), Kind: "failover", Message: message,
	})
}

// FailoverError returns the outer provider-loop signal for this upstream
// response. Its body bytes remain shared with the supplied input, and retry
// selects eligibility for the caller same-provider loop.
func (a *conversionAttempt) FailoverError(status int, body []byte, retry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: status, ResponseBody: body, RetryableOnSameProvider: retry}
}

// Output creates Chat or Responses presentation with borrowed attempt state.
// Tool restoration reads that state during output, so the adapter remains
// usable until the synchronous conversion flow finishes.
func (a *conversionAttempt) Output() forwardcore.Output {
	return a.c.ConversionOutput(a.responses, a.state)
}

// forwardResponse returns scanner and cleanup handles for a successful
// Anthropic exchange. The caller invokes Close after conversion, headers remain
// borrowed, and nonpositive MaxLineSize selects a 500 MiB limit.
func (s *Runtime) forwardResponse(resp *http.Response) forwardcore.Response {
	maxLineSize := 500 * 1024 * 1024
	if s.options.MaxLineSize > 0 {
		maxLineSize = s.options.MaxLineSize
	}
	scanner := bufio.NewScanner(resp.Body)
	// The initial scanner storage can grow up to the line cap.
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
