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

// openai_protocol_execution_chat.go dispatches Chat execution and normalizes
// Responses service-tier fields.
// Chat execution creates one attempt adapter around the selected provider and
// shared request services. The runner owns conversion and response recovery,
// while the HTTP facade captures the upstream model from a returned result,
// including a partial result that accompanies an error.

// OpenAITextExecutor binds app-owned protocol execution and shared session
// services. Each entry creates request-local adapter state while these service
// references remain available throughout synchronous forwarding and response
// observation.
type OpenAITextExecutor struct {
	Compact  *CompactExecutor      // shared Compact recovery executor
	Requests *OpenAIRequests       // shared upstream request builder
	Output   *OpenAIResponseOutput // shared response output bindings
	Grok     *GrokExecutor         // shared Grok execution adapter
	// text execution credential resolver
	Credentials *gatewayprovider.RequestCredentials
	// current fast-tier policy reader
	FastPolicy *gatewayprovider.ExecutionFastPolicy
	// Continuation and PromptCache retain session identities across attempts with
	// independent expiry rules. Continuation needs caller coordination for
	// same-key updates; PromptCache publishes the new prefix before separately
	// deleting its prior chain, so complete same-namespace updates also need coordination.
	// provider/key/prompt continuation storage
	Continuation *session.CompatResponses
	// Anthropic digest-prefix cache
	PromptCache *session.AnthropicPromptCache
	// Codex quota-header observation service
	CodexUsage *provideradapter.CodexUsageObserver
	// ForcedTemplate supplies optional Codex instructions for Messages conversion;
	// RunMessages skips template rendering when this configured text is empty.
	ForcedTemplate string
	// ResponseTTL supplies PromptCache.Bind expiry. Configure this callback before
	// Messages execution can publish a new digest-chain cache binding.
	ResponseTTL func() time.Duration
}

// Chat executes Chat Completions using the selected attempt provider and
// protocol policy. It starts with an unisolated compatibility key; callers
// retain the HTTP context until conversion, reading and model capture finish,
// then inspect both result and error.
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

// ChatWithCacheIsolation executes Chat using a new adapter for this call.
// compatPromptCacheTenantIsolated records whether the supplied key already
// carries API-key isolation, preventing RunChat from isolating that seed
// again; returned partial observations can accompany an error.
func (s *OpenAITextExecutor) ChatWithCacheIsolation(ctx context.Context, c *gin.Context, provider *gatewayprovider.ExecutionProvider, body []byte, promptCacheKey, defaultMappedModel string, compatPromptCacheTenantIsolated bool, tlsRouterMatch ...egress.TLSFingerprintRouterMatchResult) (*forwardcore.OpenAIResult, error) {
	p := &openAIChatExecutionAdapter{openAIMessagesExecutionAdapter: &openAIMessagesExecutionAdapter{s: s, c: c, provider: provider, tls: tlsRouterMatch}}
	result, err := openaiexecution.RunChat(ctx, body, promptCacheKey, defaultMappedModel, compatPromptCacheTenantIsolated, p)
	out := openaiexecution.ToForwardResult(result)
	captureResponseModel(c, out)
	return out, err
}

// normalizeResponsesRequestServiceTier replaces the request tier with its
// canonical wire value. A nil request is ignored, and unsupported values
// become empty so later serialization reflects the normalized tier.
func normalizeResponsesRequestServiceTier(req *protocolopenai.ResponsesRequest) {
	if req == nil {
		return
	}
	req.ServiceTier = protocolopenai.ServiceTierValue(req.ServiceTier)
}

// normalizeResponsesBodyServiceTier canonicalizes a nonempty service_tier
// field using byte-level JSON updates. Empty input or no nonempty tier passes
// through; unsupported tiers are deleted, recognized unchanged values retain
// the bytes, and patch errors reach the caller.
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
		// An unsupported tier must be absent in the outgoing Responses body so
		// completion attribution cannot restore the rejected client control from this value.
		trimmed, err := sjson.DeleteBytes(body, "service_tier")
		return trimmed, "", err
	}
	if normalizedServiceTier == rawServiceTier {
		return body, normalizedServiceTier, nil
	}
	// Patch the tier in the supplied bytes so other request fields retain their
	// representation while this one control adopts the protocol's canonical spelling.
	trimmed, err := sjson.SetBytes(body, "service_tier", normalizedServiceTier)
	return trimmed, normalizedServiceTier, err
}

// chatError reads and handles an upstream HTTP rejection using Chat error
// writers. The caller retains response closure and can supply the canonical
// scheduling model; CompatError may return a failover signal instead of
// writing a final error.
func (s *OpenAITextExecutor) chatError(
	resp *http.Response,
	c *gin.Context,
	provider *gatewayprovider.ExecutionProvider,
	requestedModel ...string,
) (*forwardcore.OpenAIResult, error) {
	return s.Output.CompatError(resp, c, provider, WriteForwardChatError, WriteForwardChatErrorBody, requestedModel...)
}
