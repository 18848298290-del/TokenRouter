package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Transport context can contain canonical values, compatibility aliases, or
// data supplied by another adapter. These cases keep HTTP and WebSocket labels
// normalized, return Unknown for unusable values, and leave unrecognized setter
// input out of a previously empty context.

// TestOpenAIClientTransport_SetAndGet starts with an unrecorded request and then
// records HTTP followed by WS. Reads must reflect each canonical value as
// the setter replaces the current inbound transport.
func TestOpenAIClientTransport_SetAndGet(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	require.Equal(t, OpenAIClientTransportUnknown, GetOpenAIClientTransport(c))

	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	require.Equal(t, OpenAIClientTransportHTTP, GetOpenAIClientTransport(c))

	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	require.Equal(t, OpenAIClientTransportWS, GetOpenAIClientTransport(c))
}

// TestOpenAIClientTransport_GetNormalizesRawContextValue places named values,
// mixed-case aliases, and unsupported data directly in context. Reads must
// normalize accepted representations and return Unknown for unsupported strings or types.
func TestOpenAIClientTransport_GetNormalizesRawContextValue(t *testing.T) {
	tests := []struct {
		name     string                // context value scenario
		rawValue any                   // adapter-supplied context value
		want     OpenAIClientTransport // expected normalized transport
	}{
		{
			name:     "type_value_ws",
			rawValue: OpenAIClientTransportWS,
			want:     OpenAIClientTransportWS,
		},
		{
			name:     "http_sse_alias",
			rawValue: "http_sse",
			want:     OpenAIClientTransportHTTP,
		},
		{
			name:     "sse_alias",
			rawValue: "sSe",
			want:     OpenAIClientTransportHTTP,
		},
		{
			name:     "websocket_alias",
			rawValue: "WebSocket",
			want:     OpenAIClientTransportWS,
		},
		{
			name:     "invalid_string",
			rawValue: "tcp",
			want:     OpenAIClientTransportUnknown,
		},
		{
			name:     "invalid_type",
			rawValue: 123,
			want:     OpenAIClientTransportUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(openAIClientTransportContextKey, tt.rawValue)
			require.Equal(t, tt.want, GetOpenAIClientTransport(c))
		})
	}
}

// TestOpenAIClientTransport_NilAndUnknownInput supplies nil contexts and empty
// or whitespace transport values. Calls must tolerate the missing context,
// and unusable setter input must leave an empty context without a stored key.
func TestOpenAIClientTransport_NilAndUnknownInput(t *testing.T) {
	SetOpenAIClientTransport(nil, OpenAIClientTransportHTTP)
	require.Equal(t, OpenAIClientTransportUnknown, GetOpenAIClientTransport(nil))

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	SetOpenAIClientTransport(c, OpenAIClientTransportUnknown)
	_, exists := c.Get(openAIClientTransportContextKey)
	require.False(t, exists)

	SetOpenAIClientTransport(c, OpenAIClientTransport("   "))
	_, exists = c.Get(openAIClientTransportContextKey)
	require.False(t, exists)
}

// TestSetOpenAIClientTransportHTTP records the canonical HTTP marker in a fresh
// request context. Reading it must return HTTP, which includes clients whose
// response framing may later use SSE.
func TestSetOpenAIClientTransportHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	require.Equal(t, OpenAIClientTransportHTTP, GetOpenAIClientTransport(c))
}

// TestSetOpenAIClientTransportWS records the canonical WS marker in a fresh
// request context. Reading it must return WS so ingress consumers can distinguish
// a WebSocket client from an HTTP request.
func TestSetOpenAIClientTransportWS(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	require.Equal(t, OpenAIClientTransportWS, GetOpenAIClientTransport(c))
}
