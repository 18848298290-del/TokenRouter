package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStandaloneSearchHTTPOrderAndResponse runs X search with alternate input
// spelling and an excessive result limit. It protects admission order,
// completion before release, normalized client output and the selected search
// vendor label.
func TestStandaloneSearchHTTPOrderAndResponse(t *testing.T) {
	ports := &searchHTTPStub{authenticated: true, platform: "grok"}
	c, response := searchContext(`{"input":" query ","max_results":99}`)
	NewSearchHandler(ports).XSearch(c)
	require.Equal(t, 200, response.Code)
	require.True(t, ports.isX)
	require.True(t, ports.completed)
	require.True(t, ports.released)
	require.Equal(t, []string{"model", "access", "billing", "moderation", "run", "select", "complete", "release"}, ports.calls)
	require.JSONEq(t, `{"query":"query","results":[{"url":"https://source.test","title":"source","snippet":"snippet"}],"provider":"grok-native","max_results":20}`, response.Body.String())
}

// TestStandaloneSearchHTTPRejectsInOriginalOrder supplies malformed JSON,
// incompatible field type, blank query and funding rejection. Parsing
// failures stop before dependency access, the type-error message includes
// grokStandaloneSearchRequest, and billing stops before moderation with
// Retry-After seconds.
func TestStandaloneSearchHTTPRejectsInOriginalOrder(t *testing.T) {
	t.Run("parse before auth", func(t *testing.T) {
		p := &searchHTTPStub{}
		c, r := searchContext(`{"query":`)
		NewSearchHandler(p).WebSearch(c)
		require.Equal(t, 400, r.Code)
		require.Empty(t, p.calls)
	})
	t.Run("field type error retains old struct name", func(t *testing.T) {
		p := &searchHTTPStub{}
		c, r := searchContext(`{"query":7}`)
		NewSearchHandler(p).WebSearch(c)
		require.Equal(t, 400, r.Code)
		require.JSONEq(t, `{"error":{"type":"invalid_request_error","message":"json: cannot unmarshal number into Go struct field grokStandaloneSearchRequest.query of type string"}}`, r.Body.String())
		require.Empty(t, p.calls)
	})

	t.Run("missing query before auth", func(t *testing.T) {
		p := &searchHTTPStub{}
		c, r := searchContext(`{}`)
		NewSearchHandler(p).WebSearch(c)
		require.Equal(t, 400, r.Code)
		require.Contains(t, r.Body.String(), "query is required")
		require.Empty(t, p.calls)
	})
	t.Run("billing before moderation", func(t *testing.T) {
		p := &searchHTTPStub{authenticated: true, platform: "grok", billing: &SearchHTTPFailure{Status: 429, Code: "quota", Message: "limited", RetryAfter: 3}}
		c, r := searchContext(`{"query":"q"}`)
		NewSearchHandler(p).WebSearch(c)
		require.Equal(t, 429, r.Code)
		require.Equal(t, "3", r.Header().Get("Retry-After"))
		require.Equal(t, []string{"model", "access", "billing"}, p.calls)
	})
}

// TestSearchOutputHeadersAndPerEventFlush writes one named search event
// through the bound Gin writer. The response retains SSE headers and exact
// event bytes; the recorder's Flushed flag confirms flushing after the
// complete sequence.
func TestSearchOutputHeadersAndPerEventFlush(t *testing.T) {
	c, r := searchContext(`{}`)
	out := SearchOutput{Context: c}
	out.StartStream()
	require.NoError(t, out.WriteEvent("message_stop", []byte(`{"type":"message_stop"}`)))
	out.Flush()
	require.Equal(t, 200, r.Code)
	require.Equal(t, "text/event-stream", r.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", r.Header().Get("Cache-Control"))
	require.Equal(t, "no", r.Header().Get("X-Accel-Buffering"))
	require.True(t, r.Flushed)
	require.Equal(t, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", r.Body.String())
}
