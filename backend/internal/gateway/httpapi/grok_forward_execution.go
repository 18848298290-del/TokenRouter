package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/grokforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grokForwardAdapter holds credentials and execution services for the current request.
type grokForwardAdapter struct {
	s               *GrokExecutor
	c               *gin.Context
	provider        *gatewayprovider.ExecutionProvider
	token, proxyURL string
}

// WriteGrokForwardInvalidRequest writes an OpenAI-format endpoint or tool-parameter error.
func WriteGrokForwardInvalidRequest(c *gin.Context, message, param string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message, "param": param}})
}

func (a *grokForwardAdapter) options() grokforward.Options {
	maxLine := OpenAIResponseDefaultMaxLineSize
	if a.s.Output.Options.MaxLineSize > 0 {
		maxLine = a.s.Output.Options.MaxLineSize
	}
	return grokforward.Options{ResolveAttributes: func(ctx context.Context, model string) (modelcatalog.Attributes, error) {
		result, err := gatewayprovider.ResolveModelAttributes(ctx, a.provider, model, a.s.Attributes, "openai_responses")
		return result.Attributes, err
	}, Codec: grok.BodyCodec{
		NewID: uuid.NewString,
	}, MaxLineSize: maxLine, Enter: a.s.Enter}
}

func (a *grokForwardAdapter) input(body []byte, model string, stream bool, start time.Time) grokforward.Input {
	return grokforward.Input{
		ProviderID:    a.provider.Record.ID,
		ProviderName:  a.provider.Record.Name,
		ProviderType:  a.provider.Record.Type,
		Platform:      a.provider.Record.Platform,
		OAuth:         a.provider.View().IsGrokOAuth(),
		HTTPPresent:   a.c != nil,
		Compact:       IsOpenAIResponsesCompactPath(a.c),
		Body:          body,
		OriginalModel: model,
		Stream:        stream,
		StartedAt:     start,
	}
}

func (a *grokForwardAdapter) BillingModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).ForwardModel(model, "")
}

func (a *grokForwardAdapter) UpstreamModel(model string) string {
	return gatewayprovider.ExecutionModelPolicy(a.provider).NormalizeOpenAI(model)
}

func (a *grokForwardAdapter) ImageModel(model string) bool {
	return media.IsGrokImageGenerationModel(model)
}

func (a *grokForwardAdapter) InvalidRequest(message, param string) {
	WriteGrokForwardInvalidRequest(a.c, message, param)
}

func (a *grokForwardAdapter) SetError(status int, message, detail string) {
	SetOpsUpstreamError(a.c, status, message, detail)
}

func (a *grokForwardAdapter) ClientTools(mapping bridge.ResponsesClientToolMapping) {
	SetGrokResponsesClientToolMapping(a.c, mapping)
}

func (a *grokForwardAdapter) CacheIdentity(body []byte, model string) string {
	return ResolveGrokCacheIdentity(a.c, body, "", model)
}

func (a *grokForwardAdapter) FreeCacheRoute(body, intent []byte, identity string) ([]byte, error) {
	return ApplyGrokFreeRequestToolCacheRoute(a.c, body, intent, a.provider, identity)
}

func (a *grokForwardAdapter) Credential(ctx context.Context) error {
	token, _, err := a.s.Credentials.Resolve(ctx, RequestCredentialBudget(a.c), CredentialObserver{Context: a.c}, a.provider)
	a.token = token
	return err
}

func (a *grokForwardAdapter) Detach(ctx context.Context) (context.Context, func()) {
	return gatewayprovider.DetachUpstreamContext(ctx)
}

func (a *grokForwardAdapter) ResolveProxy() {
	if a.provider.Record.ProxyID != nil && a.provider.Record.Proxy != nil {
		a.proxyURL = a.provider.Record.Proxy.URL()
	}
}

func (a *grokForwardAdapter) Build(ctx context.Context, body []byte, identity string, settings bool) (*http.Request, error) {
	return a.s.BuildResponsesRequest(ctx, a.c, a.provider, body, a.token, identity, settings)
}

func (a *grokForwardAdapter) Do(req *http.Request) (*http.Response, error) {
	return a.s.Transport.Do(req, a.proxyURL, a.provider.Record.ID, a.provider.Record.Concurrency)
}

func (a *grokForwardAdapter) ReadError(resp *http.Response) []byte {
	return a.s.Output.ReadErrorBody(resp)
}

func (a *grokForwardAdapter) Latency(value int64) {
	SetOpsLatencyMs(a.c, OpsUpstreamLatencyMsKey, value)
}

func (a *grokForwardAdapter) TransportError(ctx context.Context, err error) error {
	return a.s.Failure.Handle(ctx, a.c, a.provider, err, false)
}

func (a *grokForwardAdapter) ReplayNotice(identity bool) {
	slog.Info("grok_replay_decode_retry", "provider_id", a.provider.Record.ID, "cache_identity_present", identity)
}

func (a *grokForwardAdapter) ErrorMessage(body []byte) string {
	return logredact.SanitizeUpstreamQueries(upstream.ExtractErrorMessage(body))
}

func (a *grokForwardAdapter) Health(ctx context.Context, status int, headers http.Header, body []byte, model string, teamContext bool) grokforward.Decision {
	teamModel := ""
	if teamContext {
		teamModel = strings.TrimSpace(model)
	}
	d := gatewayprovider.ApplyGrokExecutionHealth(ctx, a.s.Health, a.provider, status, headers, body, teamModel, model)
	return grokforward.Decision{
		Generic:           d.ShouldReturnGenericError(),
		Failover:          d.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(a.provider), status, gatewayprovider.ShouldFailoverGrokResponse(status, body)),
		RetrySameProvider: d.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(a.provider), status),
	}
}

func (a *grokForwardAdapter) Observe(n grokforward.Notice) {
	AppendOpsUpstreamError(a.c, ops.OpsUpstreamErrorEvent{
		Platform:           n.Platform,
		ProviderID:         n.ProviderID,
		ProviderName:       n.ProviderName,
		UpstreamStatusCode: n.UpstreamStatusCode,
		UpstreamRequestID:  n.UpstreamRequestID,
		Kind:               n.Kind,
		Message:            n.Message,
	})
}

func (a *grokForwardAdapter) HandleError(ctx context.Context, resp *http.Response, body []byte, model string) (*forwardcore.OpenAIResult, error) {
	v, err := a.s.Output.ResponseError(ctx, resp, a.c, a.provider, body, model)
	return v, err
}

func (a *grokForwardAdapter) ShouldMarkTeam(status int, body []byte) bool {
	return grok.ShouldMarkGrokTeamModelRateLimit(status, body)
}

func (a *grokForwardAdapter) MarkTeam(model string) {
	providercore.MarkGrokTeamModelRateLimit(gatewayprovider.ExecutionRecord(a.provider), model, providercore.ResolveGrokTeamRateLimitUntil(time.Now().Add(10*time.Minute), time.Now()))
}

func (a *grokForwardAdapter) RetryMetadata(status int, body []byte) grokforward.Retry {
	retry, delay, deadline, max := gatewayprovider.GrokSameProviderRetryMetadata(a.provider, status, body)
	return grokforward.Retry{Retryable: retry, Delay: delay, Deadline: deadline, Max: max}
}

func (a *grokForwardAdapter) Failure(f grokforward.Failure) error {
	return &forwardcore.UpstreamFailoverError{
		StatusCode:                f.StatusCode,
		ResponseBody:              f.ResponseBody,
		ResponseHeaders:           f.ResponseHeaders,
		RetryableOnSameProvider:   f.RetryableOnSameProvider,
		RequestScopedTransient:    f.RequestScopedTransient,
		SameProviderRetryDelay:    f.SameProviderRetryDelay,
		SameProviderRetryDeadline: f.SameProviderRetryDeadline,
		SameProviderRetryMax:      f.SameProviderRetryMax,
	}
}

func (a *grokForwardAdapter) ObserveSuccess(ctx context.Context, headers http.Header, status int, model string) {
	a.s.Health.ObserveResponse(ctx, a.provider.View(), headers, status, model)
}

func (a *grokForwardAdapter) ReadStream(ctx context.Context, resp *http.Response, start time.Time, original, mapped string) (upstream.ResponsesObservation, error) {
	v, err := a.s.Output.ReadStreamObservation(ctx, resp, a.c, a.provider, start, original, mapped, "")
	if v == nil {
		return upstream.ResponsesObservation{}, err
	}
	return upstream.ResponsesObservation{
		UpstreamResponseModel: UpstreamResponseModelObserverFromContext(a.c).Model(),
		Usage:                 v.Usage,
		HasUsage:              v.HasUsage,
		Served:                v.Served,
		HTTPCommitted:         v.HttpCommitted,
		RetryCommitted:        v.RetryCommitted,
		ClientDisconnected:    v.ClientDisconnected,
		FirstSemanticOutput:   v.FirstSemanticOutput,
		FirstTokenMs:          v.FirstTokenMs,
		ResponseID:            v.ResponseID,
		SearchCount:           v.SearchCount,
		ImageCount:            v.ImageCount,
		ImageOutputSizes:      v.ImageOutputSizes,
	}, err
}

func (a *grokForwardAdapter) ReadNonStream(ctx context.Context, resp *http.Response, original, mapped string) (upstream.ResponsesObservation, error) {
	v, err := a.s.Output.NonStream(ctx, resp, a.c, a.provider, original, mapped)
	if err != nil {
		return upstream.ResponsesObservation{}, err
	}
	return upstream.ResponsesObservation{
		UpstreamResponseModel: UpstreamResponseModelObserverFromContext(a.c).Model(),
		Usage:                 v.Usage,
		HasUsage:              v.Usage != nil,
		Served:                v.Served,
		HTTPCommitted:         a.c.Writer.Written(),
		RetryCommitted:        IsResponseCommitted(a.c),
		ResponseID:            v.ResponseID,
		SearchCount:           v.SearchCount,
		ImageCount:            v.ImageCount,
		ImageOutputSizes:      v.ImageOutputSizes,
	}, nil
}

func (a *grokForwardAdapter) Sink() upstream.OutputSink {
	if a.c == nil {
		return nil
	}
	return ResponseSink{Writer: a.c.Writer}
}

func (a *grokForwardAdapter) Effort(body []byte, model string) *string {
	return requeststate.ExtractOpenAIReasoningEffortFromBody(body)
}

func (a *grokForwardAdapter) ReadBody(resp *http.Response) ([]byte, error) {
	return ReadUpstreamResponseBody(resp.Body, a.s.Output.Options.ReadLimit, a.c, nil)
}

func (a *grokForwardAdapter) HasTokens(usage *protocolopenai.ForwardUsage) bool {
	return protocolopenai.OpenAIUsageHasTokens(usage)
}
