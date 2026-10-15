package egress

// openai_ws_policy.go defines OpenAI upstream transport choices and constructs
// HTTP decisions with diagnostic reasons.
// OpenAI scheduling distinguishes HTTP/SSE, the Responses WebSocket protocol,
// and the broader WebSocket-ingress provider selection mode. The decision value
// retains a reason when a caller chooses HTTP, allowing request routing
// diagnostics to explain the selected protocol.

// OpenAI transport values select scheduler eligibility and the upstream
// protocol used by gateway execution for each request.
const (
	OpenAIUpstreamTransportAny                  OpenAIUpstreamTransport = ""                        // unrestricted transport selection
	OpenAIUpstreamTransportHTTPSSE              OpenAIUpstreamTransport = "http_sse"                // HTTP or SSE execution
	OpenAIUpstreamTransportResponsesWebsocketV2 OpenAIUpstreamTransport = "responses_websockets_v2" // Responses WebSocket protocol execution
	// OpenAIUpstreamTransportResponsesWebsocketV2Ingress selects providers for WS ingress, allowing ctx_pool, passthrough, and http_bridge.
	OpenAIUpstreamTransportResponsesWebsocketV2Ingress OpenAIUpstreamTransport = "responses_websockets_v2_ingress"
)

// OpenAIUpstreamTransport identifies scheduler eligibility and the selected
// upstream execution protocol for OpenAI requests.
type OpenAIUpstreamTransport string

// OpenAIWSProtocolDecision records the selected upstream transport and its
// diagnostic selection reason.
type OpenAIWSProtocolDecision struct {
	Transport OpenAIUpstreamTransport // selected upstream transport mode
	Reason    string                  // caller-supplied protocol decision reason
}

// OpenAIWSHTTPDecision selects HTTP/SSE and records the fallback reason.
func OpenAIWSHTTPDecision(reason string) OpenAIWSProtocolDecision {
	return OpenAIWSProtocolDecision{
		Transport: OpenAIUpstreamTransportHTTPSSE,
		Reason:    reason,
	}
}
