package httpapi

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/messageforward"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

type conversionReadFailure struct{}

type conversionFailingWriter struct{ gin.ResponseWriter }

func TestHandleCCBufferedFromAnthropic_ToolArgumentsAreValidJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
	}, "\n")))}

	_, err := forward.ChatBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(false, &messageforward.AttemptState{}), "gpt-5", "claude-sonnet-4.5", nil, time.Now())
	require.NoError(t, err)

	var body struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Choices, 1)
	require.Len(t, body.Choices[0].Message.ToolCalls, 1)
	args := body.Choices[0].Message.ToolCalls[0].Function.Arguments
	require.JSONEq(t, `{"city":"Paris"}`, args)
}

func TestHandleCCBufferedFromAnthropic_PreservesMessageStartCacheUsageAndReasoning(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningEffort := "high"
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":3}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ChatBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(false, &messageforward.AttemptState{}), "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.Equal(t, "hello world", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
	require.Equal(t, "stop", gjson.GetBytes(rec.Body.Bytes(), "choices.0.finish_reason").String())
}

// TestHandleCCBufferedFromAnthropic_CompactSSEFormat checks SSE fields with no space after the colon.
func TestHandleCCBufferedFromAnthropic_CompactSSEFormat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_buffered_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_c1","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{"input_tokens":15,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ChatBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(false, &messageforward.AttemptState{}), "k3", "k3", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheReadInputTokens)
	require.Equal(t, 2, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `"OK"`, "紧凑格式事件必须被解析并产出响应内容")
}

func TestHandleCCStreamingFromAnthropic_CompactSSEFormat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_stream_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_c2","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{"input_tokens":21,"cache_read_input_tokens":6,"cache_creation_input_tokens":1}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			``,
			`event:message_stop`,
			`data:{"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ChatStreaming(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(false, &messageforward.AttemptState{}), "k3", "k3", nil, time.Now(), true)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 21, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 6, result.Usage.CacheReadInputTokens)
	require.Equal(t, 1, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `[DONE]`)
}

func TestHandleCCStreamingFromAnthropic_PreservesMessageStartCacheUsageAndReasoning(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningEffort := "medium"
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":20,"cache_read_input_tokens":11,"cache_creation_input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ChatStreaming(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(false, &messageforward.AttemptState{}), "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now(), true)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 20, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 11, result.Usage.CacheReadInputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "medium", *result.ReasoningEffort)
	require.Contains(t, rec.Body.String(), `[DONE]`)
}

func TestHandleCCBufferedFromAnthropic_WritesToolCall(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_tool"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":9}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"hi\"}"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ChatBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(false, &messageforward.AttemptState{}), "gpt-5", "claude-sonnet-4.5", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "lookup", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.tool_calls.0.function.name").String())
	require.JSONEq(t, `{"q":"hi"}`, gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.tool_calls.0.function.arguments").String())
}

// conversionResponseFixture supplies a scanner, runtime, and close callback for response conversion tests.
func conversionResponseFixture(resp *http.Response) forward.Response {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 500*1024*1024)
	return forward.Response{StatusCode: resp.StatusCode, Close: func() { _ = resp.Body.Close() }, Runtime: bridge.Runtime{Now: time.Now, ReadRandom: rand.Read}, RequestID: resp.Header.Get("x-request-id"), Headers: resp.Header, Lines: scanner}
}

func conversionToolAnthropicSSEStream() string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","content":[],"model":"glm-4.7","usage":{"input_tokens":10}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"status\"}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}

func namespaceToolAnthropicStream() string {
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_namespace","type":"message","role":"assistant","content":[],"model":"claude-fable-5","stop_reason":"","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_namespace","name":"codex_app__read_thread","input":{"thread_id":"123"}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func namespaceToolMapping() bridge.ResponsesClientToolMapping {
	return bridge.ResponsesClientToolMapping{NamespaceTools: map[string]bridge.ResponsesNamespaceName{
		"codex_app__read_thread": {Namespace: "codex_app", Name: "read_thread"},
	}}
}

func TestHandleResponsesBufferedStreamingResponse_RestoresNamespaceTool(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}

	_, err := forward.ResponsesBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-fable-5", "claude-fable-5", nil, time.Now(), namespaceToolMapping())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"type":"function_call"`)
	require.Contains(t, rec.Body.String(), `"name":"read_thread"`)
	require.Contains(t, rec.Body.String(), `"namespace":"codex_app"`)
	require.NotContains(t, rec.Body.String(), `"name":"codex_app__read_thread"`)
}

func TestHandleResponsesBufferedStreamingResponse_ToolArgumentsAreValidJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(conversionToolAnthropicSSEStream()))}

	_, err := forward.ResponsesBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-fable-5", "claude-fable-5", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	require.NoError(t, err)

	var body struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Output, 1)
	require.Equal(t, "function_call", body.Output[0].Type)
	require.JSONEq(t, `{"query":"status"}`, body.Output[0].Arguments)
}

func TestHandleResponsesStreamingResponse_RestoresNamespaceTool(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}

	_, err := forward.ResponsesStreaming(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-fable-5", "claude-fable-5", nil, time.Now(), namespaceToolMapping())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `response.output_item.added`)
	require.Contains(t, rec.Body.String(), `"name":"read_thread"`)
	require.Contains(t, rec.Body.String(), `"namespace":"codex_app"`)
	require.NotContains(t, rec.Body.String(), `"name":"codex_app__read_thread"`)
}

func TestHandleResponsesBufferedStreamingResponse_PreservesMessageStartCacheUsage(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":3}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ResponsesBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `"cached_tokens":9`)
}

func TestHandleResponsesStreamingResponse_PreservesMessageStartCacheUsage(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":20,"cache_read_input_tokens":11,"cache_creation_input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ResponsesStreaming(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 20, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 11, result.Usage.CacheReadInputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `response.completed`)
}

func TestHandleResponsesBufferedStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Compact SSE omits the space after each field colon.
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_compact","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":10}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ResponsesBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"OK"`)
}

func TestHandleResponsesStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Streaming and buffered conversion accept the same compact SSE layout.
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_compact_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_compact_stream","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":15}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
			``,
			`event:message_stop`,
			`data:{"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	result, err := forward.ResponsesStreaming(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `response.completed`)
}

// TestMessagesConversionSSEFormats sends the same text and tool events with different SSE layouts.
func TestMessagesConversionSSEFormats(t *testing.T) {
	textStream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"upstream-model\",\"content\":[],\"usage\":{\"input_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"PONG\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, format := range []string{"standard", "data_only", "comments", "multiline", "event_only_type", "eof"} {
		for _, content := range []string{"text", "tool"} {
			for _, mode := range []string{"responses_stream", "responses_buffered", "chat_stream", "chat_buffered"} {
				t.Run(format+"/"+content+"/"+mode, func(t *testing.T) {
					body := textStream
					if content == "tool" {
						body = strings.NewReplacer("lookup", "codex_app__read_thread", "toolu_1", "toolu_namespace", "query", "thread_id").Replace(conversionToolAnthropicSSEStream())
					}
					body = conversionSSEFormat(t, body, format)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
					input := conversionResponseFixture(resp)
					output := NewMessageForwardBoundary(c, nil).ConversionOutput(strings.HasPrefix(mode, "responses"), &messageforward.AttemptState{})
					var result *forward.Result
					var err error
					switch mode {
					case "responses_stream":
						result, err = forward.ResponsesStreaming(input, output, "client-model", "upstream-model", nil, time.Now(), namespaceToolMapping())
					case "responses_buffered":
						result, err = forward.ResponsesBuffered(input, output, "client-model", "upstream-model", nil, time.Now(), namespaceToolMapping())
					case "chat_stream":
						result, err = forward.ChatStreaming(input, output, "client-model", "upstream-model", nil, time.Now(), true)
					case "chat_buffered":
						result, err = forward.ChatBuffered(input, output, "client-model", "upstream-model", nil, time.Now())
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 10, result.Usage.InputTokens)
					require.Equal(t, 5, result.Usage.OutputTokens)
					if content == "text" {
						require.Contains(t, rec.Body.String(), "PONG")
					} else {
						require.Contains(t, rec.Body.String(), "read_thread")
						require.Contains(t, rec.Body.String(), "toolu_namespace")
						require.Contains(t, rec.Body.String(), "thread_id")
					}
					if mode == "responses_stream" {
						completed := 0
						toolCalls := 0
						openai.ForEachOpenAISSEFrame(rec.Body.String(), func(eventType string, data []byte) {
							require.Equal(t, eventType, gjson.GetBytes(data, "type").String())
							if eventType == "response.completed" {
								completed++
								require.EqualValues(t, 5, gjson.GetBytes(data, "response.usage.output_tokens").Int())
							}
							if eventType == "response.output_item.done" && gjson.GetBytes(data, "item.type").String() == "function_call" {
								toolCalls++
								require.Equal(t, "codex_app", gjson.GetBytes(data, "item.namespace").String())
								require.JSONEq(t, `{"thread_id":"status"}`, gjson.GetBytes(data, "item.arguments").String())
							}
						})
						require.Equal(t, 1, completed)
						if content == "tool" {
							require.Equal(t, 1, toolCalls)
						}
					}
				})
			}
		}
	}
}

// conversionSSEFormat varies SSE layout, including data-only frames and event-only type fields.
func conversionSSEFormat(t *testing.T, body, format string) string {
	t.Helper()
	var result strings.Builder
	openai.ForEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		if format == "event_only_type" {
			var err error
			data, err = sjson.DeleteBytes(data, "type")
			require.NoError(t, err)
		}
		if format != "data_only" {
			_, _ = result.WriteString("event:" + eventType + "\n")
		}
		if format == "comments" {
			_, _ = result.WriteString(": heartbeat\nid: test\n")
		}
		payload := string(data)
		if format == "multiline" {
			payload = "{\ndata:" + strings.TrimPrefix(payload, "{")
		}
		_, _ = result.WriteString("data:" + payload + "\n\n")
	})
	if format == "eof" {
		return strings.TrimRight(result.String(), "\n")
	}
	return result.String()
}

func (conversionReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestMessagesConversionSSEFailures checks trailing usage, truncated JSON, and accumulated frame limits.
func TestMessagesConversionSSEFailures(t *testing.T) {
	const start = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_failure\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":10}}}\n\n"
	const usage = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":25}}\n"
	for _, mode := range []string{"responses_stream", "responses_buffered", "chat_stream", "chat_buffered"} {
		for _, failure := range []string{"complete_json", "completed", "completed_with_stop", "truncated_json", "frame_limit"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				body := start + usage
				if failure == "completed" || failure == "completed_with_stop" {
					body = strings.ReplaceAll(body, "max_tokens", "end_turn")
				}
				if failure == "completed_with_stop" {
					body += "\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				if failure == "truncated_json" {
					body = start + `data: {"type":"message_delta","usage":{"output_tokens":`
				}
				if failure == "frame_limit" {
					body = start + strings.Repeat("data: "+strings.Repeat("x", 60)+"\n", 20)
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				path := "/v1/responses"
				if strings.HasPrefix(mode, "chat") {
					path = "/v1/chat/completions"
				}
				c.Request = httptest.NewRequest(http.MethodPost, path, nil)
				before := c.Writer.Size()
				reader := io.MultiReader(strings.NewReader(body), conversionReadFailure{})
				resp := &http.Response{Body: io.NopCloser(reader)}
				input := conversionResponseFixture(resp)
				input.MaxSSEFrameBytes = 512
				output := NewMessageForwardBoundary(c, nil).ConversionOutput(strings.HasPrefix(mode, "responses"), &messageforward.AttemptState{})
				var result *forward.Result
				var err error
				switch mode {
				case "responses_stream":
					result, err = forward.ResponsesStreaming(input, output, "m", "m", nil, time.Now(), namespaceToolMapping())
				case "responses_buffered":
					result, err = forward.ResponsesBuffered(input, output, "m", "m", nil, time.Now(), namespaceToolMapping())
				case "chat_stream":
					result, err = forward.ChatStreaming(input, output, "m", "m", nil, time.Now(), true)
				case "chat_buffered":
					result, err = forward.ChatBuffered(input, output, "m", "m", nil, time.Now())
				}
				if failure == "frame_limit" {
					require.ErrorIs(t, err, forward.ErrConversionSSEFrameTooLarge)
				} else {
					require.ErrorIs(t, err, io.ErrUnexpectedEOF)
				}
				require.NotNil(t, result)
				require.Equal(t, 10, result.Usage.InputTokens)
				if failure == "complete_json" || failure == "completed" || failure == "completed_with_stop" {
					require.Equal(t, 25, result.Usage.OutputTokens)
					if strings.HasPrefix(mode, "responses") {
						status := "incomplete"
						if failure != "complete_json" {
							status = "completed"
						}
						if mode == "responses_buffered" {
							require.Equal(t, status, gjson.GetBytes(rec.Body.Bytes(), "status").String())
						} else {
							terminals := 0
							openai.ForEachOpenAISSEFrame(rec.Body.String(), func(kind string, data []byte) {
								if kind == "response.completed" || kind == "response.incomplete" {
									terminals++
									require.Equal(t, status, gjson.GetBytes(data, "response.status").String())
								}
							})
							require.Equal(t, 1, terminals)
						}
					} else {
						reason := "length"
						if failure != "complete_json" {
							reason = "stop"
						}
						require.Contains(t, rec.Body.String(), `"finish_reason":"`+reason+`"`)
					}
				} else {
					require.Zero(t, result.Usage.OutputTokens)
					require.Contains(t, rec.Body.String(), "upstream_stream_error")
					require.NotContains(t, rec.Body.String(), "response.completed")
					require.NotContains(t, rec.Body.String(), "[DONE]")
				}
				// Repeat the outer OtherFailure check; a delivered terminal event or JSON response stays unchanged.
				responseBody := rec.Body.String()
				if !OpenAIForwardErrorAlreadyCommunicated(c, before, err) {
					require.False(t, DefaultOpenAIErrorOutput().EnsureResponse(c, false, err))
				}
				require.Equal(t, responseBody, rec.Body.String())
			})
		}
	}
}

func (conversionFailingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestConversionTerminalCommit checks that partial output and write failures leave error handling to the
// caller.
func TestConversionTerminalCommit(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		if failWrite {
			c.Writer = conversionFailingWriter{ResponseWriter: c.Writer}
		}
		output := NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{})
		_, _ = output.Event("response.output_text.delta", []byte(`{"type":"response.output_text.delta","delta":"hi"}`))
		require.False(t, IsResponseCommitted(c))
		_, err := output.Event("response.failed", []byte(`{"type":"response.failed","response":{"status":"failed"}}`))
		if failWrite {
			require.ErrorIs(t, err, io.ErrClosedPipe)
			require.False(t, IsResponseCommitted(c))
		} else {
			require.NoError(t, err)
			require.True(t, IsResponseCommitted(c))
		}
	}
}
