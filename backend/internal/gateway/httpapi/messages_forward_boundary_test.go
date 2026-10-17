package httpapi

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
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
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Anthropic SSE input can carry tool arguments across fragments and report
// cache usage before its final message event. Chat and Responses conversion
// must preserve those observations while producing their distinct buffered or
// streaming envelopes. The fixtures cover compact fields and alternate frame
// layouts rather than requiring one upstream serialization.
//
// Layout rewriting preserves event payload meaning while moving the event type
// into the SSE field, splitting data across lines or removing the final
// separator. Failure readers then separate complete trailing JSON from
// truncated input and accumulated frame overflow. These cases protect usable
// usage and terminal results, and verify that partial output or failed
// terminal writes cannot claim semantic response commitment.

// conversionReadFailure injects an unexpected EOF after fixture bytes are
// consumed.
type conversionReadFailure struct{}

// conversionFailingWriter retains Gin metadata while rejecting response body
// writes.
type conversionFailingWriter struct {
	gin.ResponseWriter // underlying Gin metadata and headers
}

// conversionPartialWriter delivers half a frame with a nil error to expose
// short-write handling while retaining the original Gin header and flush methods.
type conversionPartialWriter struct {
	gin.ResponseWriter // underlying client response writer
}

// TestHandleCCBufferedFromAnthropic_ToolArgumentsAreValidJSON splits one tool
// argument object across two Anthropic deltas. Buffered Chat output must join
// those fragments into a single valid JSON arguments string.
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
						Arguments string `json:"arguments"` // JSON-encoded argument object
					} `json:"function"` // converted tool function details
				} `json:"tool_calls"` // converted function call entries
			} `json:"message"` // converted assistant message body
		} `json:"choices"` // converted Chat response choices
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Choices, 1)
	require.Len(t, body.Choices[0].Message.ToolCalls, 1)
	args := body.Choices[0].Message.ToolCalls[0].Function.Arguments
	require.JSONEq(t, `{"city":"Paris"}`, args)
}

// TestHandleCCBufferedFromAnthropic_PreservesMessageStartCacheUsageAndReasoning
// supplies initial cache buckets, later output usage and a reasoning hint.
// Buffered Chat conversion must retain each usage bucket, the hint, joined
// text and stop reason.
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

// TestHandleCCBufferedFromAnthropic_CompactSSEFormat omits spaces after SSE
// field colons in an Anthropic text stream. Buffered Chat conversion must
// still retain text and all supplied input, output and cache buckets.
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

// TestHandleCCStreamingFromAnthropic_CompactSSEFormat supplies compact
// Anthropic fields through a complete text stream. Streaming Chat conversion
// must preserve usage buckets and emit its DONE marker after interpreting
// those events.
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

// TestHandleCCStreamingFromAnthropic_PreservesMessageStartCacheUsageAndReasoning
// supplies initial cache usage, trailing output usage and a reasoning hint.
// Streaming Chat conversion must retain those values and finish the client
// stream with DONE.
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

// TestHandleCCBufferedFromAnthropic_WritesToolCall supplies one Anthropic
// lookup tool with a JSON argument delta. Buffered Chat output must preserve
// the function name and encode the supplied object in its arguments string.
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

// conversionResponseFixture adapts a response body and headers into
// synchronous conversion input. Callers provide a nonnil body; converters own
// closing it through the callback, while the scanner and header map remain
// tied to that response.
func conversionResponseFixture(resp *http.Response) forward.Response {
	scanner := bufio.NewScanner(resp.Body)
	// Keep fixture lines above the smaller cumulative-frame limits under test.
	scanner.Buffer(make([]byte, 0, 64*1024), 500*1024*1024)
	return forward.Response{StatusCode: resp.StatusCode, Close: func() {
		// Conversion invokes this cleanup after consuming fixture input; close errors
		// are excluded from the simulated read and terminal failures asserted here.
		_ = resp.Body.Close()
	}, Runtime: bridge.Runtime{Now: time.Now, ReadRandom: rand.Read}, RequestID: resp.Header.Get("x-request-id"), Headers: resp.Header, Lines: scanner}
}

// conversionToolAnthropicSSEStream returns a complete Anthropic lookup-tool
// stream with input and output usage.
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

// namespaceToolAnthropicStream returns an Anthropic tool stream using a
// flattened namespace name.
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

// namespaceToolMapping returns the client namespace identity for the flattened
// fixture tool.
func namespaceToolMapping() bridge.ResponsesClientToolMapping {
	return bridge.ResponsesClientToolMapping{NamespaceTools: map[string]bridge.ResponsesNamespaceName{
		"codex_app__read_thread": {Namespace: "codex_app", Name: "read_thread"},
	}}
}

// TestHandleResponsesBufferedStreamingResponse_RestoresNamespaceTool supplies
// the flattened Anthropic namespace tool and matching client mapping. Buffered
// Responses output must restore the namespace and short name while removing
// the flattened name.
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

// TestHandleResponsesBufferedStreamingResponse_ToolArgumentsAreValidJSON
// converts an Anthropic lookup stream into buffered Responses output. The
// single function-call item must retain its type and a valid JSON arguments
// object.
func TestHandleResponsesBufferedStreamingResponse_ToolArgumentsAreValidJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(conversionToolAnthropicSSEStream()))}

	_, err := forward.ResponsesBuffered(conversionResponseFixture(resp), NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{}), "claude-fable-5", "claude-fable-5", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	require.NoError(t, err)

	var body struct {
		Output []struct {
			Type      string `json:"type"`      // client output item discriminator
			Arguments string `json:"arguments"` // JSON-encoded argument object
		} `json:"output"` // converted Responses output items
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Output, 1)
	require.Equal(t, "function_call", body.Output[0].Type)
	require.JSONEq(t, `{"query":"status"}`, body.Output[0].Arguments)
}

// TestHandleResponsesStreamingResponse_RestoresNamespaceTool streams the
// flattened Anthropic namespace tool through Responses conversion. Client item
// events must expose the original namespace and short name without the
// flattened spelling.
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

// TestHandleResponsesBufferedStreamingResponse_PreservesMessageStartCacheUsage
// supplies initial Anthropic cache buckets and later output usage. Buffered
// Responses conversion must retain those accounting values and expose the
// cache-read count in client usage.
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

// TestHandleResponsesStreamingResponse_PreservesMessageStartCacheUsage
// converts initial cache buckets and trailing output usage in a complete
// Anthropic stream. Streaming Responses must retain every bucket and emit a
// completed response event.
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

// TestHandleResponsesBufferedStreamingResponse_CompactSSEFormat supplies
// Anthropic events with compact colon fields and a text block. Buffered
// Responses must retain the text and supplied input/output usage despite that
// layout.
func TestHandleResponsesBufferedStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Remove colon whitespace while keeping the same JSON events, isolating SSE
	// field parsing from the payload and usage preservation asserted after conversion.
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

// TestHandleResponsesStreamingResponse_CompactSSEFormat uses compact Anthropic
// colon fields in a complete text stream. Streaming Responses must preserve
// input/output usage and emit its response.completed event after conversion.
func TestHandleResponsesStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Use the same colon-without-space layout as buffered fixtures so stream
	// completion depends on parsed events rather than whitespace-specific field recognition.
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

// TestMessagesConversionSSEFormats varies six SSE layouts for text and
// namespace-tool input across four conversion modes. Usage, tool identity,
// arguments and exactly one Responses completion must remain consistent across
// the matrix.
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

// conversionSSEFormat rewrites fixture frames into the selected compatibility
// layout while retaining their event meaning. Callers supply valid
// object-shaped JSON for multiline and event-only-type cases; deletion errors
// fail the test. Unknown format values use ordinary emitted frames.
//
// Event-only-type removes the payload type while retaining the SSE event
// field, and data-only retains the payload type while omitting that field.
// Multiline inserts a second data line after the object opening; EOF removes
// final separators so conversion must consume the trailing frame. Comments add
// ignored heartbeat/id fields without altering the event payload.
func conversionSSEFormat(t *testing.T, body, format string) string {
	t.Helper()
	var result strings.Builder
	openai.ForEachOpenAISSEFrame(body, func(eventType string, data []byte) {
		// Move type identity into the SSE event field for this variant; the
		// converter must recover that identity even when the JSON member is absent.
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
		// Split after the opening brace so joining data lines reconstructs a valid
		// object while preserving the arguments and usage already carried by it.
		if format == "multiline" {
			payload = "{\ndata:" + strings.TrimPrefix(payload, "{")
		}
		_, _ = result.WriteString("data:" + payload + "\n\n")
	})
	// Omit the final frame separator to force conversion to consume valid
	// trailing data at EOF instead of requiring a subsequent blank line.
	if format == "eof" {
		return strings.TrimRight(result.String(), "\n")
	}
	return result.String()
}

// Read always returns zero bytes with the injected unexpected-EOF error.
func (conversionReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestMessagesConversionSSEFailures injects read failure after complete,
// truncated or oversized Anthropic frames in all conversion modes. Complete
// trailing usage and terminals must survive, while invalid tails retain
// partial input usage and prevent duplicate outer fallback output.
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
				// Bound accumulated frame data below the repeated-line fixture total.
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
				// Repeat the outer failure decision after conversion has presented its
				// result, protecting complete terminal or JSON output from a second error.
				responseBody := rec.Body.String()
				if !OpenAIForwardErrorAlreadyCommunicated(c, before, err) {
					require.False(t, DefaultOpenAIErrorOutput().EnsureResponse(c, false, err))
				}
				require.Equal(t, responseBody, rec.Body.String())
			})
		}
	}
}

// Write always rejects body bytes with the injected closed-pipe error.
func (conversionFailingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestConversionTerminalCommit writes a Responses delta followed by a terminal
// event through normal and failing writers. Only a successfully written
// terminal may mark semantic commitment; partial or failed writes leave it
// unset.
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

// TestMessagesConversionTiming feeds a complete Anthropic response
// through the actual Chat and Responses converters with HTTP timing installed.
// Successful content delivery and usage must coexist with both downstream
// milestones, including when all observed timestamps round to zero milliseconds.
func TestMessagesConversionTiming(t *testing.T) {
	payload := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"glm-5.3-flash","usage":{"input_tokens":21}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``, ``,
	}, "\n")
	for _, responses := range []bool{false, true} {
		name, path := "chat", "/v1/chat/completions"
		if responses {
			name, path = "responses", "/v1/responses"
		}
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(TextRequestTiming())
			var stages *timing.Stages
			router.POST(path, func(c *gin.Context) {
				stages = timing.StagesFromContext(c.Request.Context())
				SetOpsRequestContext(c, "glm-5.3-flash", true)
				MarkOpsProviderSlotAcquired(c)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}
				timing.ObserveResponse(c.Request.Context(), resp)
				output := NewMessageForwardBoundary(c, nil).ConversionOutput(responses, &messageforward.AttemptState{})
				if responses {
					result, err := forward.ResponsesStreaming(conversionResponseFixture(resp), output, "glm-5.3-flash", "glm-5.3-flash", nil, time.Now(), bridge.ResponsesClientToolMapping{})
					require.NoError(t, err)
					require.Equal(t, 4, result.Usage.OutputTokens)
				} else {
					result, err := forward.ChatStreaming(conversionResponseFixture(resp), output, "glm-5.3-flash", "glm-5.3-flash", nil, time.Now(), true)
					require.NoError(t, err)
					require.Equal(t, 4, result.Usage.OutputTokens)
				}
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "hello")
			require.True(t, recorder.Flushed)
			snapshot := stages.Snapshot()
			require.Contains(t, snapshot, "upstream_first_event_ms")
			for _, key := range []string{"first_visible_output_ms", "first_downstream_flush_ms"} {
				if _, ok := snapshot[key]; !ok {
					t.Errorf("missing %s despite delivered converted output", key)
				}
			}
		})
	}
}

// TestMessagesConversionTimingReadFailure ends the upstream before any event
// arrives. The converter must deliver its local stream error and retain the
// failure while leaving upstream-output and downstream-flush timings absent.
func TestMessagesConversionTimingReadFailure(t *testing.T) {
	for _, responses := range []bool{false, true} {
		path := "/v1/chat/completions"
		if responses {
			path = "/v1/responses"
		}
		t.Run(path, func(t *testing.T) {
			router := gin.New()
			router.Use(TextRequestTiming())
			var stages *timing.Stages
			router.POST(path, func(c *gin.Context) {
				stages = timing.StagesFromContext(c.Request.Context())
				SetOpsRequestContext(c, "model", true)
				MarkOpsProviderSlotAcquired(c)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(conversionReadFailure{})}
				timing.ObserveResponse(c.Request.Context(), resp)
				output := NewMessageForwardBoundary(c, nil).ConversionOutput(responses, &messageforward.AttemptState{})
				var err error
				if responses {
					_, err = forward.ResponsesStreaming(conversionResponseFixture(resp), output, "model", "model", nil, time.Now(), bridge.ResponsesClientToolMapping{})
				} else {
					_, err = forward.ChatStreaming(conversionResponseFixture(resp), output, "model", "model", nil, time.Now(), true)
				}
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
				require.True(t, IsResponseCommitted(c))
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
			require.Contains(t, recorder.Body.String(), "upstream_stream_error")
			require.True(t, recorder.Flushed)
			snapshot := stages.Snapshot()
			require.NotContains(t, snapshot, "upstream_first_event_ms")
			require.NotContains(t, snapshot, "first_visible_output_ms")
			require.NotContains(t, snapshot, "first_downstream_flush_ms")
		})
	}
}

// TestMessagesConversionTimingWriteFailure exercises failed and partial writes
// through the conversion adapter. Neither case may publish delivery timing or
// leave upstream provenance active when a subsequent local error is flushed.
func TestMessagesConversionTimingWriteFailure(t *testing.T) {
	for _, mode := range []string{"error", "short"} {
		t.Run(mode, func(t *testing.T) {
			router := gin.New()
			router.Use(TextRequestTiming())
			router.POST("/v1/responses", func(c *gin.Context) {
				stages := timing.StagesFromContext(c.Request.Context())
				SetOpsRequestContext(c, "model", true)
				writer, ok := c.Writer.(*timingWriter)
				require.True(t, ok)
				original := writer.ResponseWriter
				if mode == "error" {
					writer.ResponseWriter = conversionFailingWriter{ResponseWriter: original}
				} else {
					writer.ResponseWriter = conversionPartialWriter{ResponseWriter: original}
				}
				output := NewMessageForwardBoundary(c, nil).ConversionOutput(true, &messageforward.AttemptState{})
				output.BeginStream()
				payload := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
				frame := fmt.Sprintf("event: response.output_text.delta\ndata: %s\n\n", payload)
				n, err := output.Event("response.output_text.delta", payload)
				if mode == "error" {
					require.ErrorIs(t, err, io.ErrClosedPipe)
					require.Zero(t, n)
				} else {
					require.NoError(t, err)
					require.Equal(t, len(frame)/2, n)
				}
				require.False(t, IsResponseCommitted(c))
				require.Zero(t, writer.upstreamWrites.Load())
				writer.ResponseWriter = original
				_, err = output.LocalEvent("response.failed", []byte(`{"type":"response.failed","response":{"status":"failed"}}`))
				require.NoError(t, err)
				output.Flush()
				require.True(t, IsResponseCommitted(c))
				require.NotContains(t, stages.Snapshot(), "first_visible_output_ms")
				require.NotContains(t, stages.Snapshot(), "first_downstream_flush_ms")
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)))
		})
	}
}

// Write forwards half the frame and reports the underlying partial result.
// Tests use this writer to distinguish successful partial transmission from
// complete event delivery without injecting a transport error.
func (w conversionPartialWriter) Write(p []byte) (int, error) {
	// Deliver half a frame to simulate a successful short write.
	return w.ResponseWriter.Write(p[:len(p)/2])
}
