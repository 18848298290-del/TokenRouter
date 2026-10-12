package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// OpenAITextExecutor binds text protocol execution, request construction, and session state.
type OpenAITextExecutor struct {
	Compact        *CompactExecutor
	Requests       *OpenAIRequests
	Output         *OpenAIResponseOutput
	Grok           *GrokExecutor
	Credentials    *gatewayprovider.RequestCredentials
	FastPolicy     *gatewayprovider.ExecutionFastPolicy
	Continuation   *session.CompatResponses
	PromptCache    *session.AnthropicPromptCache
	CodexUsage     *provideradapter.CodexUsageObserver
	ForcedTemplate string
	ResponseTTL    func() time.Duration
}

// Chat executes a Chat Completions request using the selected provider's protocol route.
// The execution adapter handles protocol conversion and response recovery.
func (s *OpenAITextExecutor) Chat(
	ctx context.Context,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
	tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult,
) (*forwardcore.OpenAIResult, error) {
	return s.ChatWithCacheIsolation(ctx, c, provider, body, promptCacheKey, defaultMappedModel, false, tlsRouterMatch...)
}

// ChatWithCacheIsolation executes Chat conversion and recovery with the supplied cache-isolation flag.
func (s *OpenAITextExecutor) ChatWithCacheIsolation(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, compatPromptCacheTenantIsolated bool, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error) {
	p := &openAIChatExecutionAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch}}
	result, err := openaiexecution.RunChat(ctx, body, promptCacheKey, defaultMappedModel, compatPromptCacheTenantIsolated, p)
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

func normalizeResponsesRequestServiceTier(req *protocolopenai.ResponsesRequest) {
	if req == nil {
		return
	}
	req.ServiceTier = protocolopenai.ServiceTierValue(req.ServiceTier)
}

func normalizeResponsesBodyServiceTier(body []byte) ([]byte, string, error) {
	if len(body) == 0 {
		return body, "", nil
	}
	rawServiceTier := gjson.GetBytes(body, "service_tier").String()
	if rawServiceTier == "" {
		return body, "", nil
	}
	normalizedServiceTier := protocolopenai.ServiceTierValue(rawServiceTier)
	if normalizedServiceTier == "" {
		trimmed, err := sjson.DeleteBytes(body, "service_tier")
		return trimmed, "", err
	}
	if normalizedServiceTier == rawServiceTier {
		return body, normalizedServiceTier, nil
	}
	trimmed, err := sjson.SetBytes(body, "service_tier", normalizedServiceTier)
	return trimmed, normalizedServiceTier, err
}

// chatError reads an upstream error and writes an
// OpenAI Chat Completions error response.
func (s *OpenAITextExecutor) chatError(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	return s.Output.CompatError(resp, c, provider, WriteForwardChatError, WriteForwardChatErrorBody, requestedModel...)
}
