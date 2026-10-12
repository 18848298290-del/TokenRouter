package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// FilterThinkingBlocks removes thinking blocks unsuitable for direct forwarding.
// Filtering errors return the input body.
// This removes missing and placeholder signatures that can cause upstream 400 responses.
//
// Filtering rules:
//   - With thinking disabled, remove thinking-related blocks.
//   - With thinking enabled or adaptive, retain assistant thinking blocks with
//     nonempty signatures other than the placeholder; remove other thinking blocks.
//     Untyped blocks containing a thinking key are removed in either mode.
//
// The mapped model selects the protocol rules. Anthropic requests use filtering;
// passback-required families such as DeepSeek, Kimi, GLM, and MiniMax retain historical thinking blocks.
// Omitting mappedModel enables the default Anthropic filters.
func FilterThinkingBlocks(body []byte, mappedModel ...string) []byte {
	return requeststate.FilterThinkingBlocks(body, thinkingRequestOptions(mappedModel...))
}

func FilterThinkingBlocksForRetry(body []byte, mappedModel ...string) []byte {
	return requeststate.FilterThinkingBlocksForRetry(body, thinkingRequestOptions(mappedModel...))
}

func FilterSignatureSensitiveBlocksForRetry(body []byte, mappedModel ...string) []byte {
	return requeststate.FilterSignatureSensitiveBlocksForRetry(body, thinkingRequestOptions(mappedModel...))
}

// DefaultEffortForThinkingEnabled returns a display effort for passback-required
// models without native effort levels, for usage_log.reasoning_effort.
func DefaultEffortForThinkingEnabled(mappedModel string) *string {
	return requeststate.DefaultEffortForThinkingEnabled(thinkingRequestOptions(mappedModel))
}

// ApplyThinkingEnabledFallback supplies the default for a thinking-enabled,
// passback-required model when effort is absent. Existing effort values take precedence.
func ApplyThinkingEnabledFallback(effort *string, body []byte, mappedModel string) *string {
	return requeststate.ApplyThinkingEnabledFallback(effort, body, thinkingRequestOptions(mappedModel))
}

// NormalizeGLMOpenAIReasoningEffort maps OpenAI reasoning effort fields to
// GLM/z.ai levels, including low for GLM 5.3.
// It applies to mapped glm-* model names and leaves other models unchanged.
func NormalizeGLMOpenAIReasoningEffort(body []byte, mappedModel string) ([]byte, bool) {
	return requeststate.NormalizeGLMOpenAIReasoningEffort(body, thinkingRequestOptions(mappedModel))
}

// NormalizeChineseLLMThinking adjusts thinking.type for MiniMax M models.
// It maps the Anthropic SDK's enabled mode to adaptive.
func NormalizeChineseLLMThinking(body []byte, mappedModel string) ([]byte, bool) {
	return requeststate.NormalizeChineseLLMThinking(body, strings.HasPrefix(strings.ToLower(strings.TrimSpace(mappedModel)), "minimax-m"))
}

// thinkingRequestOptions selects model-family flags for requeststate's body transformations.
func thinkingRequestOptions(models ...string) requeststate.ThinkingRequestOptions {
	options := requeststate.ThinkingRequestOptions{PreFilter: true, RetryFilters: true, DummySignature: antigravity.DummyThoughtSignature}
	if len(models) == 0 {
		return options
	}
	model := models[0]
	options.PreFilter = modelidentity.ShouldPreFilterThinkingBlocks(model)
	options.RetryFilters = modelidentity.ShouldApplyRetryFilters(model)
	options.PassbackRequired = modelidentity.ResolveThinkingProtocol(model) == modelidentity.ThinkingProtocolPassbackRequired
	normalized := strings.ToLower(strings.TrimSpace(model))
	options.NativeReasoningEffort = strings.HasPrefix(normalized, "deepseek-")
	options.GLM = strings.HasPrefix(normalized, "glm-")
	options.GLM53 = normalized == "glm-5.3"
	return options
}
