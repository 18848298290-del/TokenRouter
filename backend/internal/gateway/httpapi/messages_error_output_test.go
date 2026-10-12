package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A forwarding failure can occur before output, after ordinary response bytes,
// or after a committed stream terminal. These cases protect Messages fallback
// presentation and its byte/content-type predicate, including Responses
// heartbeat output that still needs a route-specific failure event.

// TestGatewayEnsureForwardErrorResponse_WritesFallbackWhenNotWritten supplies
// a fresh writer with no semantic commitment. Fallback must report
// presentation, return HTTP 502 and encode the expected Anthropic
// upstream-error type and message.
func TestGatewayEnsureForwardErrorResponse_WritesFallbackWhenNotWritten(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	wrote := (MessagesErrorOutput{}).EnsureResponse(c, false)

	require.True(t, wrote)
	require.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	assert.Equal(t, "error", parsed["type"])
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "Upstream request failed", errorObj["message"])
}

// TestGatewayEnsureForwardErrorResponse_AppendsSSEAfterWritten first writes an
// ordinary teapot response without semantic commitment. Fallback must retain
// that HTTP status and body while appending an Anthropic SSE error frame.
func TestGatewayEnsureForwardErrorResponse_AppendsSSEAfterWritten(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.String(http.StatusTeapot, "already written")

	wrote := (MessagesErrorOutput{}).EnsureResponse(c, false)

	require.True(t, wrote)
	require.Equal(t, http.StatusTeapot, w.Code)
	assert.Contains(t, w.Body.String(), "already written")
	assert.Contains(t, w.Body.String(), `data: {"type":"error"`)
}

// TestGatewayEnsureForwardErrorResponse_SkipsCommittedSSEError writes and
// marks an existing SSE error as committed. Fallback must return false and
// leave exactly one error event in the response body.
func TestGatewayEnsureForwardErrorResponse_SkipsCommittedSSEError(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	c.Header("Content-Type", "text/event-stream")
	_, _ = c.Writer.WriteString("event: error\ndata: {\"type\":\"error\"}\n\n")
	MarkResponseCommitted(c)

	wrote := (MessagesErrorOutput{}).EnsureResponse(c, true)

	require.False(t, wrote)
	require.Equal(t, 1, strings.Count(w.Body.String(), "event: error"))
}

// TestGatewayEnsureForwardErrorResponse_ResponsesRouteAfterWrittenEmitsResponseFailed
// supplies a Responses request with only a written heartbeat. Fallback must
// retain the heartbeat and append a response.failed event using the Responses
// envelope.
func TestGatewayEnsureForwardErrorResponse_ResponsesRouteAfterWrittenEmitsResponseFailed(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	_, _ = c.Writer.WriteString(":\n\n")

	wrote := (MessagesErrorOutput{}).EnsureResponse(c, false)

	require.True(t, wrote)
	body := w.Body.String()
	assert.Contains(t, body, ":\n\n")
	assert.Contains(t, body, "event: response.failed\n")
	assert.Contains(t, body, `"type":"response.failed"`)
}

// TestGatewayForwardErrorAlreadyCommunicated compares new JSON output, SSE
// heartbeat and message output, unchanged writers and a nil forwarding error.
// Only the new non-SSE error cases must count as communicated failures.
func TestGatewayForwardErrorAlreadyCommunicated(t *testing.T) {
	t.Run("json error already written", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil)
		before := c.Writer.Size()
		c.JSON(http.StatusBadGateway, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Your Claude Code version (2.1.39) is below the minimum required version (2.1.81). Please update: npm update -g @anthropic-ai/claude-code",
			},
		})

		reported := ForwardErrorAlreadyCommunicated(c, before, errors.New("upstream error: 400 message=version too low"))

		require.True(t, reported)
		body := w.Body.String()
		assert.NotContains(t, body, `data: {"type":"error"`)
	})

	t.Run("sse ping still needs fallback", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil)
		c.Header("Content-Type", "text/event-stream")
		before := c.Writer.Size()
		_, _ = c.Writer.WriteString(":\n\n")

		reported := ForwardErrorAlreadyCommunicated(c, before, errors.New("stream read error: unexpected EOF"))

		require.False(t, reported)
	})

	t.Run("no write still needs fallback", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil)

		reported := ForwardErrorAlreadyCommunicated(c, c.Writer.Size(), errors.New("upstream request failed"))

		require.False(t, reported)
	})

	// Passing upstream JSON through c.Data changes the non-SSE writer size, so
	// outer failure handling recognizes delivered bytes and avoids an appended SSE error.
	t.Run("upstream 400 json passthrough via c.Data", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil)
		before := c.Writer.Size()
		upstreamBody := []byte(`{"type":"error","error":{"type":"upstream_error","message":"Your Claude Code version (2.1.39) is below the minimum required version (2.1.81). Please update: npm update -g @anthropic-ai/claude-code"}}`)
		c.Data(http.StatusBadRequest, "application/json", upstreamBody)

		reported := ForwardErrorAlreadyCommunicated(c, before, errors.New("upstream error: 400 message=version too low"))

		require.True(t, reported)
		body := w.Body.String()
		assert.NotContains(t, body, `data: {"type":"error"`)
		assert.Equal(t, 1, strings.Count(body, `"type":"error"`))
	})

	// A message-start event changes physical output without finishing the SSE
	// response, so an upstream failure must remain eligible for outer terminal handling.
	t.Run("streaming 400 mid-stream still needs fallback", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil)
		c.Header("Content-Type", "text/event-stream")
		before := c.Writer.Size()
		_, _ = c.Writer.WriteString("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")

		reported := ForwardErrorAlreadyCommunicated(c, before, errors.New("upstream error: 400 message=version too low"))

		require.False(t, reported)
	})

	t.Run("nil error never reports communicated", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, EndpointMessages, nil)
		c.JSON(http.StatusOK, gin.H{"ok": true})

		reported := ForwardErrorAlreadyCommunicated(c, 0, nil)

		require.False(t, reported)
	})
}
