package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// partialMessageStartSSE represents the first events of a partially delivered Messages response.
const partialMessageStartSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-5\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"

// TestStreamWrittenGuard_MessagesPath_AbortFailoverOnSSEContentWritten checks terminal error output after partial SSE
// delivery.
// The writer size detects committed content, and the error must not introduce another message_start.
func TestStreamWrittenGuard_MessagesPath_AbortFailoverOnSSEContentWritten(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	// The Gin writer starts at -1 before any bytes are written.
	sizeBeforeForward := c.Writer.Size()
	require.Equal(t, -1, sizeBeforeForward, "gin writer 初始 Size 应为 -1（未写入任何字节）")

	// Simulate partial delivery before the upstream fails.
	_, err := c.Writer.Write([]byte(partialMessageStartSSE))
	require.NoError(t, err)

	require.NotEqual(t, sizeBeforeForward, c.Writer.Size(),
		"写入 SSE 内容后 writer size 必须增加，守卫条件应为 true")

	// Simulate an upstream 403 after the stream has started.
	failoverErr := &forwardcore.UpstreamFailoverError{
		StatusCode:   http.StatusForbidden,
		ResponseBody: []byte(`{"error":{"type":"permission_error","message":"forbidden"}}`),
	}

	// Write the failure with streamStarted=true, as the committed-output guard requires.
	WriteAnthropicFailover(c, failoverErr, capability.PlatformAnthropic, true, nil, func([]byte) bool { return false }, "")

	body := w.Body.String()

	require.Contains(t, body, "event: message_start", "响应体应包含已写入的 message_start SSE 事件")

	// The terminal SSE event contains a JSON error.
	require.True(t, strings.HasSuffix(strings.TrimRight(body, "\n"), "}"),
		"响应体应以 JSON 对象结尾（SSE error event 的 data 字段）")
	require.Contains(t, body, `"type":"error"`, "响应体末尾必须包含 SSE 错误事件")

	// A second message_start would concatenate two incompatible message streams.
	firstIdx := strings.Index(body, "event: message_start")
	lastIdx := strings.LastIndex(body, "event: message_start")
	assert.Equal(t, firstIdx, lastIdx,
		"响应体中 'event: message_start' 必须只出现一次，不得因 failover 拼接导致两次")
}

// TestStreamWrittenGuard_GeminiPath_AbortFailoverOnSSEContentWritten checks terminal error output for the Gemini
// platform.
func TestStreamWrittenGuard_GeminiPath_AbortFailoverOnSSEContentWritten(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:streamGenerateContent", nil)

	sizeBeforeForward := c.Writer.Size()

	_, err := c.Writer.Write([]byte(partialMessageStartSSE))
	require.NoError(t, err)

	require.NotEqual(t, sizeBeforeForward, c.Writer.Size())

	failoverErr := &forwardcore.UpstreamFailoverError{
		StatusCode: http.StatusForbidden,
	}

	WriteAnthropicFailover(c, failoverErr, capability.PlatformGemini, true, nil, func([]byte) bool { return false }, "")

	body := w.Body.String()

	require.Contains(t, body, "event: message_start")
	require.Contains(t, body, `"type":"error"`)

	firstIdx := strings.Index(body, "event: message_start")
	lastIdx := strings.LastIndex(body, "event: message_start")
	assert.Equal(t, firstIdx, lastIdx, "Gemini 路径不得出现双 message_start")
}

// TestStreamWrittenGuard_NoByteWritten_GuardNotTriggered checks that an untouched writer allows failover.

func TestStreamWrittenGuard_NoByteWritten_GuardNotTriggered(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	sizeBeforeForward := c.Writer.Size()

	// An upstream failure before any SSE bytes are written leaves the writer size unchanged.

	guardTriggered := c.Writer.Size() != sizeBeforeForward
	require.False(t, guardTriggered,
		"未写入任何字节时，守卫条件必须为 false，应允许正常 failover 继续")
}
