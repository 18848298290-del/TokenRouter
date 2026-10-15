package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/modelidentity"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// thinking_request.go resolves model-family options for thinking-history
// filtering and effort conversion.
// Thinking-history filtering depends on the mapped model's replay format, so
// passback families retain their historical blocks while Anthropic-oriented
// paths can clean invalid signatures. These wrappers resolve family flags for
// requeststate before filtering, retry cleanup, display-effort inference, or
// native effort conversion; omitted model arguments select the default
// Anthropic filters.

// FilterThinkingBlocks delegates pre-forward cleanup using the first mapped
// model's replay rules, or default Anthropic rules when omitted. Declined or
// failed filtering returns the supplied body, and passback-required model
// families retain their history.
func FilterThinkingBlocks(body []byte, mappedModel ...string) []byte {
	return requeststate.FilterThinkingBlocks(body, thinkingRequestOptions(mappedModel...))
}

// FilterThinkingBlocksForRetry delegates retry cleanup when the mapped model's
// retry-filter gate permits it. Omitted model arguments use the default gate;
// disabled families and failed protocol rewrites retain the supplied bytes.
func FilterThinkingBlocksForRetry(body []byte, mappedModel ...string) []byte {
	return requeststate.FilterThinkingBlocksForRetry(body, thinkingRequestOptions(mappedModel...))
}

// FilterSignatureSensitiveBlocksForRetry delegates signature-sensitive history
// cleanup under the mapped model's retry gate. Omitted models enable the
// default filter, while passback families retain their historical
// representation according to requeststate's options.
func FilterSignatureSensitiveBlocksForRetry(body []byte, mappedModel ...string) []byte {
	return requeststate.FilterSignatureSensitiveBlocksForRetry(body, thinkingRequestOptions(mappedModel...))
}

// DefaultEffortForThinkingEnabled returns a high display-effort pointer for
// passback-required models that lack native effort. Other families return nil;
// this usage-reporting default does not itself change outgoing request fields.
func DefaultEffortForThinkingEnabled(mappedModel string) *string {
	return requeststate.DefaultEffortForThinkingEnabled(thinkingRequestOptions(mappedModel))
}

// ApplyThinkingEnabledFallback retains a supplied effort pointer, including
// empty text, before examining body thinking state. With no pointer, enabled
// or adaptive thinking can select the mapped family's display default.
func ApplyThinkingEnabledFallback(effort *string, body []byte, mappedModel string) *string {
	return requeststate.ApplyThinkingEnabledFallback(effort, body, thinkingRequestOptions(mappedModel))
}

// NormalizeGLMOpenAIReasoningEffort applies built-in GLM effort mapping using
// the mapped model flags. GLM 5.3 retains its low-level exception, while
// declined conversions or failed byte updates return the supplied body with
// false.
func NormalizeGLMOpenAIReasoningEffort(body []byte, mappedModel string) ([]byte, bool) {
	return requeststate.NormalizeGLMOpenAIReasoningEffort(body, thinkingRequestOptions(mappedModel))
}

// NormalizeChineseLLMThinking converts exact enabled thinking.type to adaptive
// for mapped minimax-m models. Other spellings or families, and failed byte
// updates, return the supplied body with false.
func NormalizeChineseLLMThinking(body []byte, mappedModel string) ([]byte, bool) {
	return requeststate.NormalizeChineseLLMThinking(body, strings.HasPrefix(strings.ToLower(strings.TrimSpace(mappedModel)), "minimax-m"))
}

// thinkingRequestOptions selects replay and family flags from the first
// optional mapped model. With no model it enables initial and retry filters;
// passback identity, native DeepSeek effort, GLM family, and exact GLM 5.3 are
// resolved independently.
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
