package egress

const (
	OpenAIUpstreamTransportAny                  OpenAIUpstreamTransport = ""
	OpenAIUpstreamTransportHTTPSSE              OpenAIUpstreamTransport = "http_sse"
	OpenAIUpstreamTransportResponsesWebsocketV2 OpenAIUpstreamTransport = "responses_websockets_v2"
	// OpenAIUpstreamTransportResponsesWebsocketV2Ingress selects providers for WS ingress, allowing ctx_pool, passthrough, and http_bridge.
	OpenAIUpstreamTransportResponsesWebsocketV2Ingress OpenAIUpstreamTransport = "responses_websockets_v2_ingress"
)

// OpenAIUpstreamTransport identifies an OpenAI upstream transport protocol.
type OpenAIUpstreamTransport string

// OpenAIWSProtocolDecision contains the selected protocol and its reason.
type OpenAIWSProtocolDecision struct {
	Transport OpenAIUpstreamTransport
	Reason    string
}

// OpenAIWSHTTPDecision selects HTTP/SSE and records the fallback reason.
func OpenAIWSHTTPDecision(reason string) OpenAIWSProtocolDecision {
	return OpenAIWSProtocolDecision{
		Transport: OpenAIUpstreamTransportHTTPSSE,
		Reason:    reason,
	}
}
