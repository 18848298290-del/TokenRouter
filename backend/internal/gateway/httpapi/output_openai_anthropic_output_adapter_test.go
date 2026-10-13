package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	httptestkit "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/testkit"
	openaiexecution "github.com/TokenFlux/TokenRouter/internal/gateway/provider/openaiforward"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// nativeAnthropicReaderFixture binds HTTP output options for native
// protocol-reader assertions.
type nativeAnthropicReaderFixture struct {
	output *OpenAIResponseOutput // bound conversion output settings
}

// newNativeAnthropicHangTestService binds an inactivity budget measured in
// seconds. It supplies the default scanner limit and configured output options;
// tests use these callbacks with their own response writer and upstream pipe.
func newNativeAnthropicHangTestService(intervalSec int) *nativeAnthropicReaderFixture {
	return &nativeAnthropicReaderFixture{output: &OpenAIResponseOutput{Options: OpenAIResponseOptions{Configured: true, StreamDataIntervalTimeout: intervalSec, MaxLineSize: OpenAIResponseDefaultMaxLineSize}}}
}

// newHangingUpstreamResponse returns a successful response whose body reads
// from a synchronous pipe. The reader remains blocked until the writer supplies
// data or closes; callers retain both endpoints and close them during fixture
// cleanup.
func newHangingUpstreamResponse() (*http.Response, *io.PipeReader, *io.PipeWriter) {
	pr, pw := io.Pipe()
	return &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}, pr, pw
}

// miniAnthropicSSEStream returns a complete text stream with terminal token
// usage.
func miniAnthropicSSEStream() string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"glm-4.7","usage":{"input_tokens":10,"output_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
		"",
	}, "\n")
}

// toolAnthropicSSEStream returns a tool-use stream carrying JSON argument
// fragments.
func toolAnthropicSSEStream() string {
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

// TestCCStreamingFromNativeAnthropic_HangTimesOut supplies a pipe with no
// upstream events and a one-second inactivity budget. It requires interval
// timeout, a result carrier and completion within a bounded observation period.
func TestCCStreamingFromNativeAnthropic_HangTimesOut(t *testing.T) {
	svc := newNativeAnthropicHangTestService(1)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp, pr, pw := newHangingUpstreamResponse()
	start := time.Now()
	res, err := svc.handleCCStreamingFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, start, true)
	_ = pw.Close()
	_ = pr.Close()

	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected stream timeout error, got %v", err)
	}
	if res == nil {
		t.Fatalf("expected result carrying accumulated usage")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler did not respect interval bound: %v", elapsed)
	}
}

// TestCCBufferedFromNativeAnthropic_HangTimesOut supplies a stalled pipe during
// buffered Chat conversion. It requires interval timeout within the observation
// budget and the safe stream-timeout response body.
func TestCCBufferedFromNativeAnthropic_HangTimesOut(t *testing.T) {
	svc := newNativeAnthropicHangTestService(1)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp, pr, pw := newHangingUpstreamResponse()
	start := time.Now()
	_, err := svc.handleCCBufferedFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, start)
	_ = pw.Close()
	_ = pr.Close()

	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected stream timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler did not respect interval bound: %v", elapsed)
	}
	if !strings.Contains(rec.Body.String(), "Upstream stream data interval timeout") {
		t.Fatalf("expected 502 error body, got %q", rec.Body.String())
	}
}

// TestResponsesStreamingFromNativeAnthropic_HangTimesOut stalls native
// Anthropic input before converted Responses events arrive. It requires an
// interval timeout, a result carrier and completion within the observation
// bound.
func TestResponsesStreamingFromNativeAnthropic_HangTimesOut(t *testing.T) {
	svc := newNativeAnthropicHangTestService(1)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp, pr, pw := newHangingUpstreamResponse()
	start := time.Now()
	res, err := svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, start, bridge.ResponsesClientToolMapping{})
	_ = pw.Close()
	_ = pr.Close()

	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected stream timeout error, got %v", err)
	}
	if res == nil {
		t.Fatalf("expected result carrying accumulated usage")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("handler did not respect interval bound: %v", elapsed)
	}
}

// TestResponsesStreamingFromNativeAnthropicClientDisconnectDrainsUsage rejects
// the first downstream write while a complete native stream remains readable.
// It requires disconnected-result attribution and final input/output usage
// retained after upstream draining.
func TestResponsesStreamingFromNativeAnthropicClientDisconnectDrainsUsage(t *testing.T) {
	svc := newNativeAnthropicHangTestService(5)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Writer = &httptestkit.FailingWriter{ResponseWriter: c.Writer, FailAfter: 0}

	resp, pr, pw := newHangingUpstreamResponse()
	// Feed the synchronous pipe from another goroutine so writes can progress with
	// the reader, then close its writer to expose end-of-stream.
	go func() {
		_, _ = pw.Write([]byte(miniAnthropicSSEStream()))
		_ = pw.Close()
	}()
	defer func() { _ = pr.Close() }()

	res, err := svc.handleResponsesStreamingFromNativeAnthropic(
		resp,
		c,
		"glm-4.7",
		"glm-4.7",
		"glm-4.7",
		nil,
		time.Now(),
		bridge.ResponsesClientToolMapping{},
	)
	if err != nil {
		t.Fatalf("断开后排水不应失败：%v", err)
	}
	if res == nil || !res.ClientDisconnect {
		t.Fatalf("expected disconnected result, got %+v", res)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
		t.Fatalf("expected drained usage 10/5, got %+v", res.Usage)
	}
}

// TestCCStreamingFromNativeAnthropic_HappyPathStillConverts supplies a complete
// native text stream to Chat output. It requires successful conversion with
// visible text and a DONE terminator in the client stream.
func TestCCStreamingFromNativeAnthropic_HappyPathStillConverts(t *testing.T) {
	svc := newNativeAnthropicHangTestService(5)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp, pr, pw := newHangingUpstreamResponse()
	// Feed the synchronous pipe from another goroutine so writes can progress with
	// the reader, then close its writer to expose end-of-stream.
	go func() {
		_, _ = pw.Write([]byte(miniAnthropicSSEStream()))
		_ = pw.Close()
	}()
	defer func() { _ = pr.Close() }()

	res, err := svc.handleCCStreamingFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected result")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello") {
		t.Fatalf("expected converted text chunk, got %q", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected [DONE] terminator, got %q", body)
	}
}

// TestCCBufferedFromNativeAnthropic_HappyPathStillConverts buffers a complete
// native text stream for Chat output. It requires converted text and final
// native input/output token counts in the result.
func TestCCBufferedFromNativeAnthropic_HappyPathStillConverts(t *testing.T) {
	svc := newNativeAnthropicHangTestService(5)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp, pr, pw := newHangingUpstreamResponse()
	// Feed the synchronous pipe from another goroutine so writes can progress with
	// the reader, then close its writer to expose end-of-stream.
	go func() {
		_, _ = pw.Write([]byte(miniAnthropicSSEStream()))
		_ = pw.Close()
	}()
	defer func() { _ = pr.Close() }()

	res, err := svc.handleCCBufferedFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected result")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello") {
		t.Fatalf("expected converted text in buffered response, got %q", body)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
		t.Fatalf("expected usage 10/5, got %+v", res.Usage)
	}
}

// TestCCBufferedFromNativeAnthropic_ToolArgumentsAreValidJSON converts native
// tool-use argument fragments into a buffered Chat response. It requires one
// tool call whose argument string is valid JSON and matches the supplied
// object.
func TestCCBufferedFromNativeAnthropic_ToolArgumentsAreValidJSON(t *testing.T) {
	svc := newNativeAnthropicHangTestService(5)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(toolAnthropicSSEStream())), Header: http.Header{}}

	_, err := svc.handleCCBufferedFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body struct {
		Choices []struct { // decoded Chat result choices
			Message struct { // decoded assistant message
				ToolCalls []struct { // decoded assistant tool calls
					Function struct { // decoded function-call payload
						Arguments string `json:"arguments"` // JSON
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Choices) != 1 || len(body.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %s", rec.Body.String())
	}
	args := body.Choices[0].Message.ToolCalls[0].Function.Arguments
	if !json.Valid([]byte(args)) || args != `{"query":"status"}` {
		t.Fatalf("expected valid tool arguments, got %q", args)
	}
}

// TestResponsesBufferedFromNativeAnthropic_ToolArgumentsAreValidJSON converts
// native tool-use fragments into buffered Responses output. It requires one
// function-call item and an argument string matching the complete valid JSON
// object.
func TestResponsesBufferedFromNativeAnthropic_ToolArgumentsAreValidJSON(t *testing.T) {
	svc := newNativeAnthropicHangTestService(5)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(toolAnthropicSSEStream())), Header: http.Header{}}

	_, err := svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(), bridge.ResponsesClientToolMapping{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body struct {
		Output []struct { // decoded Responses output items
			Type      string `json:"type"`      // output item type
			Arguments string `json:"arguments"` // JSON
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Output) != 1 || body.Output[0].Type != "function_call" {
		t.Fatalf("expected one function call, got %s", rec.Body.String())
	}
	if args := body.Output[0].Arguments; !json.Valid([]byte(args)) || args != `{"query":"status"}` {
		t.Fatalf("expected valid tool arguments, got %q", args)
	}
}

// handleCCBufferedFromNativeAnthropic delegates native stream collection and
// Chat conversion using deferred HTTP output. It passes model identities and
// timing separately, converts the result for assertions and leaves response
// cleanup with the fixture caller.
func (s *nativeAnthropicReaderFixture) handleCCBufferedFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*forwardcore.OpenAIResult, error) {
	result, err := openaiexecution.ChatFromAnthropicBuffered(resp, upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), s.output.AnthropicOptions(c, WriteForwardChatError), originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
	return openaiexecution.ToForwardResult(result), err
}

// handleCCStreamingFromNativeAnthropic delegates incremental native-to-Chat
// conversion through deferred output. It passes includeUsage to control stream
// usage events, returns the converted result and error, and leaves upstream
// body cleanup with the test caller.
func (s *nativeAnthropicReaderFixture) handleCCStreamingFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	includeUsage bool,
) (*forwardcore.OpenAIResult, error) {
	result, err := openaiexecution.ChatFromAnthropicStreaming(resp, upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), s.output.AnthropicOptions(c, WriteForwardChatError), originalModel, billingModel, upstreamModel, reasoningEffort, startTime, includeUsage)
	return openaiexecution.ToForwardResult(result), err
}

// handleResponsesBufferedFromNativeAnthropic delegates native stream collection
// to buffered Responses conversion. Client tool mappings accompany model
// identities and timing; the caller retains upstream cleanup while result
// conversion preserves the reader error for assertions.
func (s *nativeAnthropicReaderFixture) handleResponsesBufferedFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientToolMapping bridge.ResponsesClientToolMapping,
) (*forwardcore.OpenAIResult, error) {
	result, err := openaiexecution.ResponsesFromAnthropicBuffered(resp, upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), s.output.AnthropicOptions(c, writeTextResponsesError), originalModel, billingModel, upstreamModel, reasoningEffort, startTime, clientToolMapping)
	return openaiexecution.ToForwardResult(result), err
}

// handleResponsesStreamingFromNativeAnthropic delegates incremental conversion
// with the supplied client tool mapping. The shared reader owns drain and
// timeout behavior; tests retain body cleanup and inspect both the converted
// result and error.
func (s *nativeAnthropicReaderFixture) handleResponsesStreamingFromNativeAnthropic(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientToolMapping bridge.ResponsesClientToolMapping,
) (*forwardcore.OpenAIResult, error) {
	result, err := openaiexecution.ResponsesFromAnthropicStreaming(resp, upstream.NewDeferredOutputContext(ResponseSink{Writer: c.Writer}), s.output.AnthropicOptions(c, writeTextResponsesError), originalModel, billingModel, upstreamModel, reasoningEffort, startTime, clientToolMapping)
	return openaiexecution.ToForwardResult(result), err
}
