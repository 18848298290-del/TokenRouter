package modelidentity

import "github.com/TokenFlux/TokenRouter/internal/modelcatalog"

const (
	// ThinkingProtocolUnknown leaves thinking blocks intact when the protocol family is unknown.
	ThinkingProtocolUnknown ThinkingProtocol = iota

	// ThinkingProtocolAnthropicStrict removes thinking blocks with missing or invalid signatures.
	ThinkingProtocolAnthropicStrict

	// ThinkingProtocolPassbackRequired replays thinking blocks unchanged for compatible upstreams.
	ThinkingProtocolPassbackRequired
)

// ThinkingProtocol describes how the upstream handles historical thinking blocks.
// Anthropic requires valid signatures; compatible providers with passback rules
// require the historical blocks to be replayed unchanged.
type ThinkingProtocol int

// ResolveThinkingProtocol infers thinking-block rules from the upstream model ID.
//
// Anthropic paths pass the mapped upstream model. Gemini Messages compatibility
// retries pass the original client model because the filters modify the Anthropic body.
func ResolveThinkingProtocol(modelID string) ThinkingProtocol {
	rules := (modelcatalog.View{}).Resolve(modelcatalog.Target{Model: modelID, UpstreamProtocol: "anthropic_messages"}, modelcatalog.Attributes{}).Rules
	switch rules.SignatureMode {
	case "passback":
		return ThinkingProtocolPassbackRequired
	case "anthropic":
		return ThinkingProtocolAnthropicStrict
	default:
		return ThinkingProtocolUnknown
	}
}

// ShouldPreFilterThinkingBlocks reports whether invalid thinking blocks should be removed before forwarding.
func ShouldPreFilterThinkingBlocks(modelID string) bool {
	return ResolveThinkingProtocol(modelID) == ThinkingProtocolAnthropicStrict
}

// ShouldRectifyThinkingSignatureError reports whether the model permits signature repair after a 400 response.
func ShouldRectifyThinkingSignatureError(modelID string) bool {
	return ResolveThinkingProtocol(modelID) == ThinkingProtocolAnthropicStrict
}

// ShouldApplyRetryFilters reports whether retries may rewrite thinking and tool blocks.
func ShouldApplyRetryFilters(modelID string) bool {
	return ResolveThinkingProtocol(modelID) == ThinkingProtocolAnthropicStrict
}
