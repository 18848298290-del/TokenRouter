package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

func TestOpenAIHandleStreamingAwareError_JSONEscaping(t *testing.T) {
	tests := []struct {
		name    string
		errType string
		message string
	}{
		{
			name:    "包含双引号的消息",
			errType: "server_error",
			message: `upstream returned "invalid" response`,
		},
		{
			name:    "包含反斜杠的消息",
			errType: "server_error",
			message: `path C:\Users\test\file.txt not found`,
		},
		{
			name:    "包含双引号和反斜杠的消息",
			errType: "upstream_error",
			message: `error parsing "key\value": unexpected token`,
		},
		{
			name:    "包含换行符的消息",
			errType: "server_error",
			message: "line1\nline2\ttab",
		},
		{
			name:    "普通消息",
			errType: "upstream_error",
			message: "Upstream service temporarily unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

			DefaultOpenAIErrorOutput().StreamError(c, http.StatusBadGateway, tt.errType, tt.message, true)

			body := w.Body.String()

			// Check SSE framing: event: error\ndata: {JSON}\n\n.
			assert.True(t, strings.HasPrefix(body, "event: error\n"), "应以 'event: error\\n' 开头")
			assert.True(t, strings.HasSuffix(body, "\n\n"), "应以 '\\n\\n' 结尾")

			lines := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n")
			require.Len(t, lines, 2, "应有 event 行和 data 行")
			dataLine := lines[1]
			require.True(t, strings.HasPrefix(dataLine, "data: "), "第二行应以 'data: ' 开头")
			jsonStr := strings.TrimPrefix(dataLine, "data: ")

			var parsed map[string]any
			err := json.Unmarshal([]byte(jsonStr), &parsed)
			require.NoError(t, err, "JSON 应能被成功解析，原始 JSON: %s", jsonStr)

			errorObj, ok := parsed["error"].(map[string]any)
			require.True(t, ok, "应包含 error 对象")
			assert.Equal(t, tt.errType, errorObj["type"])
			assert.Equal(t, tt.message, errorObj["message"])
		})
	}
}

func TestOpenAIHandleStreamingAwareErrorWithCode_EmitsStableClassification(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	DefaultOpenAIErrorOutput().WriteStreamingErrorWithCode(
		c,
		http.StatusBadGateway,
		"upstream_error",
		openai.OpenAIUpstreamHTTP2StreamErrorCode,
		"Upstream HTTP/2 stream failed",
		true,
		true,
	)

	body := w.Body.String()
	require.Contains(t, body, "event: error\n")
	require.Equal(t, "upstream_error", gjson.Get(body[strings.Index(body, "{"):], "error.type").String())
	require.Equal(t, openai.OpenAIUpstreamHTTP2StreamErrorCode, gjson.Get(body[strings.Index(body, "{"):], "error.code").String())
	require.NotContains(t, body, "stream ID")

	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.True(t, streamErr.CountTowardsSLA)
	require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
}

func TestOpenAIHandleStreamingAwareError_NonStreaming(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	DefaultOpenAIErrorOutput().StreamError(c, http.StatusBadGateway, "upstream_error", "test error", false)

	assert.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "test error", errorObj["message"])
}

func TestOpenAIEnsureForwardErrorResponse_WritesFallbackWhenNotWritten(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	wrote := DefaultOpenAIErrorOutput().EnsureFallback(c, false)

	require.True(t, wrote)
	require.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "Upstream request failed", errorObj["message"])
}

// TestOpenAIEnsureForwardErrorResponse_AppendsSSEAfterWritten checks SSE error delivery after response output.
// An already-written response requires in-stream error handling.
func TestOpenAIEnsureForwardErrorResponse_AppendsSSEAfterWritten(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.String(http.StatusTeapot, "already written")

	wrote := DefaultOpenAIErrorOutput().EnsureFallback(c, false)

	require.True(t, wrote, "must attempt to communicate the failure to the client via SSE")
	// The committed HTTP status stays fixed while the SSE error is appended.
	require.Equal(t, http.StatusTeapot, w.Code)
	assert.Contains(t, w.Body.String(), "already written")
	// Non-Responses routes use event: error.
	assert.Contains(t, w.Body.String(), "event: error\n")
}

// TestOpenAIEnsureForwardErrorResponse_ResponsesRouteAfterWrittenEmitsResponseFailed checks response.failed
// after a heartbeat.
func TestOpenAIEnsureForwardErrorResponse_ResponsesRouteAfterWrittenEmitsResponseFailed(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	// Simulate heartbeat bytes already written to the response.
	_, _ = c.Writer.WriteString(":\n\n")

	wrote := DefaultOpenAIErrorOutput().EnsureFallback(c, false)

	require.True(t, wrote)
	body := w.Body.String()
	assert.Contains(t, body, ":\n\n", "earlier ping bytes preserved")
	assert.Contains(t, body, "event: response.failed\n", "appended a Responses terminal event")
	assert.Contains(t, body, `"type":"response.failed"`)
	assert.Contains(t, body, `"code":"upstream_error"`)
	assert.Contains(t, body, "Upstream request failed")
}

func TestOpenAIEnsureForwardErrorResponse_CompactKeepaliveOnlyWritesResponseFailed(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)
	MarkOpenAICompactClientStream(c)

	stop := StartOpenAICompactSSEKeepalive(c, 5*time.Millisecond)
	defer stop()
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	// Simulate a commit marker set before any protocol event was delivered.
	MarkResponseCommitted(c)

	require.True(t, DefaultOpenAIErrorOutput().EnsureFallback(c, false))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "event: response.failed\n")
	require.NotContains(t, w.Body.String(), "event: error\n")
}

// TestOpenAIEnsureForwardErrorResponse_AfterDeltaAppendsSingleValidResponseFailed checks one valid failure
// terminal event.
func TestOpenAIEnsureForwardErrorResponse_AfterDeltaAppendsSingleValidResponseFailed(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, EndpointResponses, nil)

	delta := `{"type":"response.output_text.delta","delta":"ok","sequence_number":1}`
	_, err := c.Writer.WriteString("event: response.output_text.delta\ndata: " + delta + "\n\n")
	require.NoError(t, err)

	require.True(t, DefaultOpenAIErrorOutput().EnsureFallback(c, true))

	frames := strings.Split(strings.TrimSuffix(w.Body.String(), "\n\n"), "\n\n")
	require.Len(t, frames, 2)
	errorEvents := 0
	for _, frame := range frames {
		lines := strings.Split(frame, "\n")
		require.Len(t, lines, 2)
		require.True(t, strings.HasPrefix(lines[0], "event: "))
		require.True(t, strings.HasPrefix(lines[1], "data: "))

		eventType := strings.TrimPrefix(lines[0], "event: ")
		data := strings.TrimPrefix(lines[1], "data: ")
		require.True(t, json.Valid([]byte(data)), "each downstream SSE frame must contain valid JSON")
		var event struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(data), &event))
		require.Equal(t, eventType, event.Type)
		if eventType == "response.failed" {
			errorEvents++
		}
	}
	require.Equal(t, 1, errorEvents)
}

func TestOpenAIEnsureForwardErrorResponse_ImageJSONKeepaliveWritesSingleJSONFallback(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	before := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)
	require.Equal(t, before, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	require.False(t, OpenAIForwardErrorAlreadyCommunicated(c, before, errors.New("read upstream response: unexpected EOF")))

	wrote := DefaultOpenAIErrorOutput().EnsureFallback(c, false)

	require.True(t, wrote)
	require.Equal(t, http.StatusOK, w.Code, "heartbeat already committed the status")
	require.True(t, json.Valid(w.Body.Bytes()), w.Body.String())
	require.NotContains(t, w.Body.String(), "event:")
	require.NotContains(t, w.Body.String(), "data:")

	decoder := json.NewDecoder(strings.NewReader(w.Body.String()))
	var payload map[string]any
	require.NoError(t, decoder.Decode(&payload))
	require.ErrorIs(t, decoder.Decode(&payload), io.EOF)
	require.Equal(t, "upstream_error", gjson.Get(w.Body.String(), "error.type").String())
	require.Equal(t, "Upstream request failed", gjson.Get(w.Body.String(), "error.message").String())
}

func TestOpenAIEnsureForwardErrorResponse_ImageJSONKeepalivePreservesCompletedJSON(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	before := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	require.Eventually(t, c.Writer.Written, time.Second, time.Millisecond)
	c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "aW1hZ2U="}}})
	completedBody := w.Body.String()
	require.True(t, json.Valid([]byte(completedBody)), completedBody)
	require.Greater(t, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), before)
	require.False(t, OpenAIForwardErrorAlreadyCommunicated(c, before, errors.New("read upstream trailer: unexpected EOF")))

	wrote := DefaultOpenAIErrorOutput().EnsureFallback(c, false)

	require.False(t, wrote, "the completed Images JSON already communicated the response")
	require.Equal(t, completedBody, w.Body.String())
	require.NotContains(t, w.Body.String(), "event:")
	require.NotContains(t, w.Body.String(), "data:")

	decoder := json.NewDecoder(strings.NewReader(w.Body.String()))
	var payload map[string]any
	require.NoError(t, decoder.Decode(&payload))
	require.ErrorIs(t, decoder.Decode(&payload), io.EOF)
	require.Equal(t, "aW1hZ2U=", gjson.Get(w.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIEnsureForwardErrorResponse_FastImageJSONKeepalivePreservesCompletedJSON(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, time.Hour)
	defer stop()
	before := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "ZmFzdC1pbWFnZQ=="}}})
	completedBody := w.Body.String()
	require.True(t, json.Valid([]byte(completedBody)), completedBody)
	require.Greater(t, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), before)
	require.False(t, OpenAIForwardErrorAlreadyCommunicated(c, before, errors.New("read upstream trailer: unexpected EOF")))

	wrote := DefaultOpenAIErrorOutput().EnsureFallback(c, false)

	require.False(t, wrote, "fast completed Images JSON already communicated the response")
	require.Equal(t, completedBody, w.Body.String())
	require.NotContains(t, w.Body.String(), "event:")
	require.NotContains(t, w.Body.String(), "data:")

	decoder := json.NewDecoder(strings.NewReader(w.Body.String()))
	var payload map[string]any
	require.NoError(t, decoder.Decode(&payload))
	require.ErrorIs(t, decoder.Decode(&payload), io.EOF)
	require.Equal(t, "ZmFzdC1pbWFnZQ==", gjson.Get(w.Body.String(), "data.0.b64_json").String())
}
