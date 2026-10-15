package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// openai_ws_transport.go selects native Responses WebSocket transport or an
// HTTP bridge.
// Responses WebSocket transport is selected from the provider's declared
// protocol support before dialing an upstream connection. Providers supporting
// WS use websocket v2; absent providers and other protocol configurations
// receive an HTTP bridge decision with a reason for transport diagnostics.

// ResolveOpenAIWSTransport chooses websocket v2 when the provider declares
// Responses WS support. Nil providers and providers without that protocol
// receive the HTTP bridge decision, allowing transport callers to use a single
// selection result.
func ResolveOpenAIWSTransport(value *provider.Record) egress.OpenAIWSProtocolDecision {
	if value != nil && value.SupportsResponsesWS() {
		return egress.OpenAIWSProtocolDecision{Transport: egress.OpenAIUpstreamTransportResponsesWebsocketV2, Reason: "native_protocol"}
	}
	// The HTTP bridge performs a Responses turn through HTTP when native WS is
	// absent; provider selection and turn ownership remain with its gateway caller.
	return egress.OpenAIWSHTTPDecision("protocol_http")
}
