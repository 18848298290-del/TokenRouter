package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/compact"
	"github.com/TokenFlux/TokenRouter/internal/gateway/media"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	forward "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

const openAIResponsesRejectedFieldRetryBudgetContextKey = "openai_responses_rejected_field_retry_budget"

type openAIPassthroughExecutionAdapter struct {
	*openAIMessagesExecutionAdapter
}

func (p *openAIPassthroughExecutionAdapter) Profile() forward.MessagesProfile {
	return forward.MessagesProfile{Profile: openAIForwardProfile(p.provider), ID: p.provider.Record.ID, Shadow: p.provider.View().IsShadow()}
}

func (p *openAIPassthroughExecutionAdapter) CompactPath() bool {
	return IsOpenAIResponsesCompactPath(p.c)
}

// ForwardModel applies single-hop model rules, giving Compact-specific configuration priority.
func (p *openAIPassthroughExecutionAdapter) ForwardModel(model string, compact bool) string {
	if compact && p.s.Compact != nil {
		if configured := p.s.Compact.ResolveModel(p.provider, model); configured != "" {
			return configured
		}
	}
	return gatewayadapter.ExecutionModelPolicy(p.provider).OpenAIUpstream(model, compact)
}

func (p *openAIPassthroughExecutionAdapter) InstructionsRejection(model string, body []byte) string {
	return openai.DetectOpenAIPassthroughInstructionsRejectReason(model, body)
}

func (p *openAIPassthroughExecutionAdapter) PolicyDenied() {
	MarkOpsClientBusinessLimited(p.c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
}

func (p *openAIPassthroughExecutionAdapter) LogInstructionsRejected(ctx context.Context, model, reason string, body []byte) {
	logOpenAIPassthroughInstructionsRejected(ctx, p.c, p.provider, model, reason, body)
}

func (p *openAIPassthroughExecutionAdapter) Reject(status int, kind, message, param string) {
	WriteOpenAIForwardRejection(p.c, status, kind, message, param)
}

func (p *openAIPassthroughExecutionAdapter) CodexModel(model string) bool {
	return openai.IsOpenAICodexModel(model)
}

func (p *openAIPassthroughExecutionAdapter) OAuthBody(body []byte, compact bool) ([]byte, bool, error) {
	return openai.NormalizeOpenAIPassthroughOAuthBody(body, compact)
}

func (p *openAIPassthroughExecutionAdapter) ProviderIdentityRaw(body []byte) ([]byte, bool, error) {
	return openai.ApplyCodexProviderIdentityClientMetadataRaw(body, provideradapter.CodexIdentityNamespace(CodexIdentityRecord(p.c, p.provider.View())), APIKeyIDFromContext(p.c))
}

func (p *openAIPassthroughExecutionAdapter) StageFingerprint(ids *openai.FingerprintIDs) {
	StageCodexFingerprintIDs(p.c, ids)
}

func (p *openAIPassthroughExecutionAdapter) Fingerprint() *openai.FingerprintIDs {
	var headers http.Header
	if p.c != nil && p.c.Request != nil {
		headers = p.c.Request.Header
	}
	return provideradapter.CodexFingerprintIDsFromRequest(p.provider.View(), headers)
}

func (p *openAIPassthroughExecutionAdapter) FingerprintBody(body []byte, ids *openai.FingerprintIDs) ([]byte, bool, error) {
	return openai.ApplyCodexFingerprintClientMetadataRaw(body, ids)
}

func (p *openAIPassthroughExecutionAdapter) HasContext() bool {
	return p.c != nil
}

func (p *openAIPassthroughExecutionAdapter) LiteHeader() bool {
	return gatewayadapter.ImageIntent().IsOpenAIResponsesLiteHeader(p.c.GetHeader(media.ResponsesLiteHeader))
}

func (p *openAIPassthroughExecutionAdapter) LitePayloadFlag(body []byte) bool {
	return gatewayadapter.ImageIntent().IsOpenAIResponsesLiteWebSocketPayload(body)
}

func (p *openAIPassthroughExecutionAdapter) CompatibilityBody(body []byte, lite bool) ([]byte, bool, error) {
	return gatewayadapter.NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, gatewayadapter.ExecutionProtocolRecord(p.provider), lite)
}

func (p *openAIPassthroughExecutionAdapter) ReservedToolNames(body []byte) ([]byte, map[string]string, bool, error) {
	return openai.AliasOpenAIOAuthReservedToolNamesBody(body)
}

func (p *openAIPassthroughExecutionAdapter) MergeToolNames(value map[string]string) {
	MergeCodexToolNameReverse(p.c, value)
}

func (p *openAIPassthroughExecutionAdapter) NeedsClientTools(body []byte) bool {
	return protocolbridge.NeedsOpenAIResponsesClientToolAdaptation(body)
}

func (p *openAIPassthroughExecutionAdapter) AdaptClientTools(body []byte) ([]byte, error) {
	updated, mapping, err := protocolbridge.AdaptOpenAIResponsesClientTools(body)
	if err == nil {
		SetOpenAIResponsesClientToolMapping(p.c, mapping)
	}
	return updated, err
}

func (p *openAIPassthroughExecutionAdapter) NormalizeLite(body []byte) ([]byte, bool, error) {
	return gatewayadapter.NormalizeResponsesLiteForProvider(p.provider.View(), body)
}

func (p *openAIPassthroughExecutionAdapter) ApplyFastPass(ctx context.Context, model string, body []byte) ([]byte, error) {
	updated, err := tierpolicy.ApplyBody(body, p.s.FastPolicy.Input(ctx, p.provider, model))
	if blocked, ok := errors.AsType[*tierpolicy.BlockedError](err); ok {
		WriteFastPolicyBlockedResponse(p.c, blocked)
	}
	return updated, err
}

func (p *openAIPassthroughExecutionAdapter) ImageIntent(model string, canonical []byte, policy string, body []byte, invalidated bool) bool {
	return ResolveOpenAIPassthroughImageIntent(p.c, model, canonical, policy, body, invalidated, gatewayadapter.ImageIntent().IsImageGenerationIntent)
}

func (p *openAIPassthroughExecutionAdapter) ExplicitImageIntent(model string, body []byte) bool {
	return gatewayadapter.ImageIntent().IsExplicitImageGenerationIntent(media.OpenAIResponsesEndpoint, model, body)
}

func (p *openAIPassthroughExecutionAdapter) ImageAllowed() bool {
	return routing.GroupAllowsResponsesImages(openAIRequestGroup(p.c))
}

func (p *openAIPassthroughExecutionAdapter) FeatureDenied() {
	MarkOpsClientBusinessLimited(p.c, OpsClientBusinessLimitedReasonLocalFeatureGate)
}

func (p *openAIPassthroughExecutionAdapter) ImagePermissionMessage() string {
	return media.ImageGenerationPermissionMessage
}

func (p *openAIPassthroughExecutionAdapter) ImageBilling(body []byte, model string) (forward.ImageBilling, error) {
	v, err := gatewayadapter.ImageIntent().ResolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, model)
	return forward.ImageBilling{Model: v.Model, SizeTier: v.SizeTier, InputSize: v.InputSize}, err
}

func (p *openAIPassthroughExecutionAdapter) UpstreamError(status int, message string) {
	SetOpsUpstreamError(p.c, status, message, "")
}

func (p *openAIPassthroughExecutionAdapter) Log(format string, args ...any) {
	logging.LegacyPrintf("service.openai_gateway", format, args...)
}

func (p *openAIPassthroughExecutionAdapter) WarnTimeoutHeaders(ctx context.Context) {
	if p.c == nil || p.c.Request == nil {
		return
	}
	if h := collectOpenAIPassthroughTimeoutHeaders(p.c.Request.Header); len(h) > 0 {
		log := logging.FromContext(ctx).With(zap.String("component", "service.openai_gateway"), zap.Int64("provider_id", p.provider.Record.ID), zap.Strings("timeout_headers", h))
		if p.s.Requests.AllowTimeoutHeaders() {
			log.Warn("OpenAI passthrough 透传请求包含超时相关请求头，且当前配置为放行，可能导致上游提前断流")
		} else {
			log.Warn("OpenAI passthrough 检测到超时相关请求头，将按配置过滤以降低断流风险")
		}
	}
}

func (p *openAIPassthroughExecutionAdapter) AccessToken(ctx context.Context) (string, error) {
	token, _, err := p.s.Requests.Credentials.Resolve(ctx, gatewayadapter.ExecutionRecord(p.provider))
	return token, err
}

func (p *openAIPassthroughExecutionAdapter) PrepareTransportPass() {
	p.proxyURL = ""
	if p.provider.Record.ProxyID != nil && p.provider.Record.Proxy != nil {
		p.proxyURL = p.provider.Record.Proxy.URL()
	}
}

func (p *openAIPassthroughExecutionAdapter) MarkPassthrough() {
	if p.c != nil {
		p.c.Set("openai_passthrough", true)
	}
}

func (p *openAIPassthroughExecutionAdapter) RetryState(body []byte) *openai.ResponsesRejectedFieldRetryState {
	return openAIResponsesRejectedFieldRetryStateForRequest(p.c, body)
}

func (p *openAIPassthroughExecutionAdapter) UpstreamModelObserved(model string) {
	SetOpsUpstreamModel(p.c, model)
}

func (p *openAIPassthroughExecutionAdapter) BuildPass(ctx context.Context, body []byte, token string) (*http.Request, error) {
	return p.s.Requests.BuildPassthrough(ctx, p.c, p.provider, body, token, p.tls...)
}

func (p *openAIPassthroughExecutionAdapter) SendPass(r *http.Request) (*http.Response, error) {
	resetResponseModel(p.c)
	return p.s.Requests.Transport.DoWithTLS(r, p.proxyURL, p.provider.Record.ID, p.provider.Record.Concurrency, p.s.Requests.TLSProfile(p.provider, p.tls...))
}

func (p *openAIPassthroughExecutionAdapter) Latency(d time.Duration) {
	SetOpsLatencyMs(p.c, OpsUpstreamLatencyMsKey, d.Milliseconds())
}

func (p *openAIPassthroughExecutionAdapter) TransportErrorPass(ctx context.Context, err error) error {
	return p.s.Requests.Failure.Handle(ctx, p.c, p.provider, err, true)
}

func (p *openAIPassthroughExecutionAdapter) CompactRetry(model string, body []byte, status int, message string, payload []byte, tried bool) ([]byte, string, bool) {
	return p.s.Compact.Prepare(p.c, p.provider, model, body, status, message, payload, tried)
}

func (p *openAIPassthroughExecutionAdapter) CompactObserved(r *http.Response, body []byte, message string) {
	p.s.Compact.Observe(p.c, p.provider, r, body, message, true)
}

func (p *openAIPassthroughExecutionAdapter) ErrorCode(body []byte) string {
	return upstream.ExtractErrorCode(body)
}

func (p *openAIPassthroughExecutionAdapter) ShouldFailover(status int, body []byte) bool {
	return shouldFailoverOpenAIPassthroughResponse(p.provider, status, body)
}

func (p *openAIPassthroughExecutionAdapter) FailoverError(ctx context.Context, r *http.Response, body, payload []byte) error {
	return p.s.Output.PassthroughFailoverError(ctx, r, p.c, p.provider, body, payload)
}

func (p *openAIPassthroughExecutionAdapter) ErrorResponsePass(ctx context.Context, r *http.Response, body, payload []byte) error {
	return p.s.Output.PassthroughError(ctx, r, p.c, p.provider, body, payload)
}

func (p *openAIPassthroughExecutionAdapter) WrapResponseBody(r *http.Response) {
	if mapping, ok := OpenAIResponsesClientToolMapping(p.c); ok && openai.IsEventStreamResponse(r.Header) {
		limit := OpenAIResponseDefaultMaxLineSize
		if p.s.Output.Options.MaxLineSize > 0 {
			limit = p.s.Output.Options.MaxLineSize
		}
		r.Body = upstream.NewResponsesClientToolStreamBody(r.Body, mapping, limit)
	}
}

func (p *openAIPassthroughExecutionAdapter) ObserveProvenance(h http.Header) {
	if ExtractCodexTurnState(h) != "" {
		p.s.Requests.Turns.Commit(p.c, p.provider, h)
	}
}

func (p *openAIPassthroughExecutionAdapter) ResponseOptions(ctx context.Context) openai.PassthroughOptions {
	return p.s.Output.PassthroughOptions(ctx, p.c, p.provider)
}

func (p *openAIPassthroughExecutionAdapter) CompactFromSignal(model string, body []byte, err error, tried bool, r *http.Response) ([]byte, string, bool) {
	return p.s.Compact.ApplySignal(p.c, p.provider, model, body, err, tried, r)
}

func (p *openAIPassthroughExecutionAdapter) CompactSignal(err error) (forward.CompactFailure, bool) {
	v, ok := compact.AsFailure(err)
	if !ok {
		return forward.CompactFailure{}, false
	}
	return forward.CompactFailure{Message: v.Message, Payload: v.Payload}, true
}

func (p *openAIPassthroughExecutionAdapter) CompactErrorResponse(r *http.Response, v forward.CompactFailure) (*http.Response, []byte) {
	return CompactFallbackErrorResponse(r, &compact.Failure{Message: v.Message, Payload: v.Payload})
}

func (p *openAIPassthroughExecutionAdapter) BindOwner(ctx context.Context, id string) {
	p.s.Output.BindResponseProvider(ctx, p.c, p.provider, id)
}

func (p *openAIPassthroughExecutionAdapter) ObservedServiceTier() string {
	return ObservedUpstreamResponseServiceTier(p.c)
}

// openAIRequestGroup reads the group from the request's API key.
func openAIRequestGroup(c *gin.Context) *routing.Group {
	if c == nil {
		return nil
	}
	v, ok := c.Get("api_key")
	if !ok {
		return nil
	}
	key, ok := v.(*apikey.APIKey)
	if !ok || key == nil {
		return nil
	}
	return key.Group
}

// openAIResponsesRejectedFieldRetryStateForRequest creates body deduplication state for one provider attempt.
// Attempts share the inbound request's retry budget.
func openAIResponsesRejectedFieldRetryStateForRequest(c *gin.Context, initialBody []byte) *openai.ResponsesRejectedFieldRetryState {
	var budget *openai.ResponsesRejectedFieldRetryBudget
	if c != nil {
		if existing, ok := c.Get(openAIResponsesRejectedFieldRetryBudgetContextKey); ok {
			budget, _ = existing.(*openai.ResponsesRejectedFieldRetryBudget)
		}
	}
	if budget == nil {
		budget = &openai.ResponsesRejectedFieldRetryBudget{}
		if c != nil {
			c.Set(openAIResponsesRejectedFieldRetryBudgetContextKey, budget)
		}
	}
	return openai.NewOpenAIResponsesRejectedFieldRetryStateWithBudget(initialBody, budget)
}
