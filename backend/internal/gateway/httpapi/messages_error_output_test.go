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

// TestGatewayEnsureForwardErrorResponse_AppendsSSEAfterWritten checks error delivery after response bytes
// were written.
// Non-Responses routes use the Anthropic data: {"type":"error"} format.
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

// TestGatewayEnsureForwardErrorResponse_ResponsesRouteAfterWrittenEmitsResponseFailed checks that
// an Anthropic-backed Responses request receives response.failed after a heartbeat.
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

	// A passed-through upstream 400 JSON body is already a complete error response.
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

	// An upstream 400 after SSE output still needs a terminal frame; HTTP status is already committed.
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
