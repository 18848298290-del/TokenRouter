package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// ShouldFlattenOpenAIResponsesNamespaces reports whether to flatten Codex namespace tools.
// OAuth Responses retains namespaces by default for functions.<namespace>.<tool> lookup.
// Compact requests and the provider compatibility setting enable flattening.
// WSv2 retains namespaces because its events bypass HTTP response restoration, unless passthrough selects HTTP.
func ShouldFlattenOpenAIResponsesNamespaces(
	provider *ExecutionProvider,
	transport egress.OpenAIUpstreamTransport,
	passthroughEnabled bool,
	compactPath bool,
) bool {
	if provider == nil || !provider.View().IsOpenAIOAuthLike() {
		return false
	}
	if !compactPath && !provider.View().IsOpenAIResponsesFlattenNamespacesEnabled() {
		return false
	}
	if transport == egress.OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

// ShouldStripOpenAIResponsesInputNamespaces reports whether HTTP forwarding cleans namespaces from history items.
// WSv2 sends the field unchanged and uses WebSocket recovery rules for errors.
func ShouldStripOpenAIResponsesInputNamespaces(provider *ExecutionProvider, transport egress.OpenAIUpstreamTransport, passthroughEnabled bool) bool {
	if provider == nil || (!provider.View().IsOpenAIOAuthLike() && !provider.View().IsOpenAIApiKey()) {
		return false
	}
	if transport == egress.OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

// ShouldKeepOpenAIResponsesToolCallNamespaces reports whether tool-call items keep
// their namespace while other history items are cleaned.
//
// The upstream endpoint and tool declarations determine which fields are accepted:
//   - /backend-api/codex/responses resolves historical calls by namespace and returns 400:
//     `Missing namespace for function_call '...'. Round-trip the model's
//     function_call item with its namespace field included.` (issue #4761 discussion).
//     OAuth requests therefore retain the field outside compact mode.
//   - Compact rejects the field with 400 `Unknown parameter:
//     input[N].namespace` (issue #4761), so compact requests remove it.
//   - API Key requests use the standard Responses schema unless they declare namespace
//     tools. Such declarations require matching namespaces on historical calls
//     to prevent Missing namespace errors from a custom upstream.
//   - Flattened calls use flat names, so their obsolete namespaces are removed.
func ShouldKeepOpenAIResponsesToolCallNamespaces(
	provider *ExecutionProvider,
	transport egress.OpenAIUpstreamTransport,
	passthroughEnabled bool,
	compactPath bool,
	body []byte,
) bool {
	if provider == nil {
		return false
	}
	if compactPath {
		return false
	}
	if provider.View().IsOpenAIApiKey() {
		return protocolbridge.HasOpenAIResponsesNamespaceToolDeclaration(body)
	}
	if !provider.View().IsOpenAIOAuthLike() {
		return false
	}
	return !ShouldFlattenOpenAIResponsesNamespaces(provider, transport, passthroughEnabled, compactPath)
}
