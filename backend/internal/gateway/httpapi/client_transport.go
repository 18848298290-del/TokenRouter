package httpapi

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// client_transport.go records and retrieves normalized inbound OpenAI transport
// labels. Inbound HTTP and WebSocket clients can reach the same OpenAI
// execution path. Request context stores their normalized transport separately
// from upstream transport selection, allowing later execution and observation
// to identify how this client entered the gateway.

// Client transport values identify inbound request framing after accepted aliases
// are normalized; the context key stores their canonical string representation.
const (
	OpenAIClientTransportUnknown OpenAIClientTransport = ""     // transport is unrecorded or unrecognized
	OpenAIClientTransportHTTP    OpenAIClientTransport = "http" // HTTP or SSE client
	OpenAIClientTransportWS      OpenAIClientTransport = "ws"   // WebSocket client connection

	openAIClientTransportContextKey = "openai_client_transport" // normalized inbound transport
)

// OpenAIClientTransport identifies the normalized transport of the inbound client request.
type OpenAIClientTransport string

// SetOpenAIClientTransport records the inbound transport for this request.
// It ignores nil contexts and unrecognized values, leaving any existing record
// intact. Accepted values are normalized before their canonical string is stored
// for later execution and observation.
func SetOpenAIClientTransport(c *gin.Context, transport OpenAIClientTransport) {
	if c == nil {
		return
	}
	normalized := normalizeOpenAIClientTransport(transport)
	if normalized == OpenAIClientTransportUnknown {
		return
	}
	c.Set(openAIClientTransportContextKey, string(normalized))
}

// GetOpenAIClientTransport returns the recorded inbound transport, or Unknown if absent or invalid.
// It accepts either the named transport type or a string from context and
// normalizes known aliases. Nil contexts and values of other dynamic types
// yield Unknown for callers deciding whether an ingress transport was recorded.
func GetOpenAIClientTransport(c *gin.Context) OpenAIClientTransport {
	if c == nil {
		return OpenAIClientTransportUnknown
	}
	raw, ok := c.Get(openAIClientTransportContextKey)
	if !ok || raw == nil {
		return OpenAIClientTransportUnknown
	}

	switch v := raw.(type) {
	case OpenAIClientTransport:
		return normalizeOpenAIClientTransport(v)
	case string:
		return normalizeOpenAIClientTransport(OpenAIClientTransport(v))
	default:
		return OpenAIClientTransportUnknown
	}
}

// normalizeOpenAIClientTransport maps accepted aliases to canonical client transports.
// Case and surrounding whitespace are ignored. SSE aliases map to HTTP,
// websocket maps to WS, and any other value yields the empty Unknown sentinel.
func normalizeOpenAIClientTransport(transport OpenAIClientTransport) OpenAIClientTransport {
	// These aliases describe inbound framing. Keep SSE in the HTTP class so
	// callers decide WebSocket policy from the client's connection type rather than its label.
	switch strings.ToLower(strings.TrimSpace(string(transport))) {
	case string(OpenAIClientTransportHTTP), "http_sse", "sse":
		return OpenAIClientTransportHTTP
	case string(OpenAIClientTransportWS), "websocket":
		return OpenAIClientTransportWS
	default:
		return OpenAIClientTransportUnknown
	}
}
