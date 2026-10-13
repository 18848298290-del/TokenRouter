package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	protocolbridge "github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
)

// Namespace tool declarations and historical calls need compatible naming on
// the selected Responses path. OAuth-like requests normally retain namespaces,
// Compact selects flattened declarations and stripped history, and API Key
// requests retain call namespaces when their current body declares namespace
// tools.
//
// Native WSv2 forwards its namespace representation when passthrough is
// disabled, so the flattening gate leaves those declarations intact even when
// the provider compatibility setting is enabled. HTTP-compatible paths can
// restore flattened names through the response bridge. These decisions are
// request hints for the shared protocol transformations, with separate gates
// for declaration flattening, general input cleanup, and call-history
// retention.

// ShouldFlattenOpenAIResponsesNamespaces permits OAuth-like flattening for
// Compact or the saved compatibility setting. Native WSv2 retains namespaces
// unless passthrough selects its HTTP-compatible path, because HTTP tool-name
// restoration is required for flattened declarations.
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
	// Native WS events bypass HTTP name restoration. Keep their namespace shape
	// until the caller selects a passthrough path that performs HTTP-compatible restoration.
	if transport == egress.OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

// ShouldStripOpenAIResponsesInputNamespaces selects history namespace cleanup
// for OpenAI OAuth-like or API Key targets. Native WSv2 keeps the input
// representation when passthrough is disabled, letting WS recovery handle
// rejected namespace fields.
func ShouldStripOpenAIResponsesInputNamespaces(provider *ExecutionProvider, transport egress.OpenAIUpstreamTransport, passthroughEnabled bool) bool {
	if provider == nil || (!provider.View().IsOpenAIOAuthLike() && !provider.View().IsOpenAIApiKey()) {
		return false
	}
	// Native WS events bypass HTTP name restoration. Keep their namespace shape
	// until the caller selects a passthrough path that performs HTTP-compatible restoration.
	if transport == egress.OpenAIUpstreamTransportResponsesWebsocketV2 && !passthroughEnabled {
		return false
	}
	return true
}

// ShouldKeepOpenAIResponsesToolCallNamespaces chooses whether call history
// retains namespaces while other items are cleaned. Nil targets and Compact
// requests return false; API Key requests retain them when namespace tools are
// declared.
//
// OAuth-like requests keep call namespaces when their declaration policy is
// not flattened, including native WSv2 paths. Flattened declarations instead
// use flat names and remove obsolete call namespaces. This pairing keeps
// history names interpretable under the current declarations while Compact
// uses its narrower input shape; the caller applies the actual byte
// transformation through the protocol bridge.
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
