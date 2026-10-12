package httpapi

import (
	"context"
	"errors"
	"net/http"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	upstreamopenai "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type openAIRawChatAdapter struct{ *openAIRawFallbackAdapter }

func (p *openAIRawChatAdapter) Profile() openaiexecution.MessagesProfile {
	return openaiexecution.MessagesProfile{Profile: openAIForwardProfile(p.provider), ID: p.provider.Record.ID, GrokOAuth: p.provider.View().IsGrokOAuth()}
}

func (p *openAIRawChatAdapter) Error(status int, kind, message string) {
	WriteForwardChatError(p.c, status, kind, message)
}

func (p *openAIRawChatAdapter) ReplaceModel(b []byte, m string) []byte {
	return openai.ReplaceModelInBody(b, m)
}

func (p *openAIRawChatAdapter) FastRaw(ctx context.Context, m string, b []byte) ([]byte, error) {
	updated, err := tierpolicy.ApplyBody(b, p.s.FastPolicy.Input(ctx, p.provider, m))
	if blocked, ok := errors.AsType[*tierpolicy.BlockedError](err); ok {
		MarkOpsClientBusinessLimited(p.c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		WriteForwardChatError(p.c, http.StatusForbidden, "permission_error", blocked.Message)
	}
	return updated, err
}

func (p *openAIRawChatAdapter) StripViewImage(b []byte) ([]byte, error) {
	return grok.StripRedundantGrokChatViewImageTool(b)
}

func (p *openAIRawChatAdapter) RawCredential(ctx context.Context) (string, string, error) {
	return p.s.Credentials.Resolve(ctx, RequestCredentialBudget(p.c), CredentialObserver{Context: p.c}, p.provider)
}

func (p *openAIRawChatAdapter) BridgeImages(ctx context.Context, b []byte, key string) ([]byte, openai.ForwardUsage, bool, error) {
	updated, usage, changed, err := p.s.Grok.BridgeComposerImages(ctx, p.c, p.provider, b, key)
	var failover *forwardcore.UpstreamFailoverError
	if err != nil && !errors.As(err, &failover) && p.c != nil && p.c.Writer != nil && !p.c.Writer.Written() {
		WriteForwardChatError(p.c, http.StatusBadGateway, "upstream_error", err.Error())
	}
	return updated, usage, changed, err
}

func (p *openAIRawChatAdapter) StripGrokCacheKey(b []byte) ([]byte, error) {
	return grok.StripGrokChatPromptCacheKey(b)
}

func (p *openAIRawChatAdapter) GrokEffort(b []byte, m string) ([]byte, error) {
	result, err := gatewayprovider.ResolveModelAttributes(p.c.Request.Context(), p.provider, m, p.s.modelAttributes(), "openai_chat_completions")
	if err != nil {
		return nil, err
	}
	codec := gatewayprovider.GrokBodyCodec()
	codec.Attributes = func(string) modelcatalog.Attributes { return result.Attributes }
	return codec.NormalizeGrokChatReasoningEffort(b, m)
}

func (p *openAIRawChatAdapter) OllamaBody(b []byte) []byte {
	return gatewayprovider.ApplyOllamaCloudRawChatCompletionsRequest(p.provider, b)
}

func (p *openAIRawChatAdapter) RawTarget() (string, error) {
	return p.s.Requests.RawChatURL(p.provider)
}

func (p *openAIRawChatAdapter) UserAgent() string     { return p.provider.View().GetOpenAIUserAgent() }
func (p *openAIRawChatAdapter) GrokUserAgent() string { return grok.DefaultGrokUpstreamUserAgent() }
func (p *openAIRawChatAdapter) SendRaw(ctx context.Context, url string, b []byte, stream bool, key, ua, identity string) (*http.Response, error) {
	resetResponseModel(p.c)
	return p.s.Requests.SendChat(ctx, p.c, p.provider, url, b, stream, key, ua, identity, p.tls...)
}

func (p *openAIRawChatAdapter) ChatErrorResponse(r *http.Response, m string) (*openaiexecution.Result, error) {
	v, e := p.s.chatError(r, p.c, p.provider, m)
	return openaiexecution.FromForwardResult(v), e
}

func (p *openAIRawChatAdapter) RawOptions(r *http.Response, billing, model string, tier *string) upstreamopenai.RawResponseOptions {
	return p.s.Output.RawOptions(p.c, r, p.provider, billing, model, tier, WriteForwardChatError)
}

// GrokDecision returns the three retry decisions needed by orchestration from the platform health
// classification.
func (p *openAIRawChatAdapter) GrokDecision(ctx context.Context, resp *http.Response, b []byte, m string) openaiexecution.RawGrokDecision {
	d := gatewayprovider.ApplyGrokExecutionHealth(ctx, p.s.Grok.Health, p.provider, resp.StatusCode, resp.Header, b, "", m)
	return openaiexecution.RawGrokDecision{Failover: d.ShouldFailover(gatewayprovider.ExecutionErrorPolicy(p.provider), resp.StatusCode, gatewayprovider.ShouldFailoverGrokResponse(resp.StatusCode, b)), Generic: d.ShouldReturnGenericError(), RetrySame: d.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(p.provider), resp.StatusCode)}
}

func (p *openAIRawChatAdapter) ObserveGrokError(r *http.Response, msg, kind string) {
	AppendOpsUpstreamError(p.c, ops.OpsUpstreamErrorEvent{Platform: p.provider.Record.Platform, ProviderID: p.provider.Record.ID, ProviderName: p.provider.Record.Name, UpstreamStatusCode: r.StatusCode, UpstreamRequestID: requeststate.FirstNonEmpty(r.Header.Get("x-request-id"), r.Header.Get("xai-request-id")), Kind: kind, Message: msg})
}

func (p *openAIRawChatAdapter) GrokRetry(status int, b []byte) openaiexecution.RawGrokRetry {
	retry, delay, deadline, max := gatewayprovider.GrokSameProviderRetryMetadata(p.provider, status, b)
	return openaiexecution.RawGrokRetry{Retryable: retry, Delay: delay, Deadline: deadline, Max: max}
}

func (p *openAIRawChatAdapter) GrokFailover(r *http.Response, b []byte, retry openaiexecution.RawGrokRetry, healthRetry bool) error {
	return &forwardcore.UpstreamFailoverError{StatusCode: r.StatusCode, ResponseBody: b, ResponseHeaders: r.Header.Clone(), RetryableOnSameProvider: retry.Retryable || healthRetry, RequestScopedTransient: retry.Retryable && r.StatusCode == http.StatusTooManyRequests, SameProviderRetryDelay: retry.Delay, SameProviderRetryDeadline: retry.Deadline, SameProviderRetryMax: retry.Max}
}
