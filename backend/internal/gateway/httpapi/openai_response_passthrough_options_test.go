package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	httptestkit "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/testkit"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	responseupstream "github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

type passthroughFlushTestErrorBody struct {
	payload []byte
	err     error
	sent    bool
}

// TestHandlePassthroughSSEToJSON_CompactRawOutputItemDoneRepairsEmptyTerminalOutput checks raw compaction
// recovery into empty terminal output.
func TestHandlePassthroughSSEToJSON_CompactRawOutputItemDoneRepairsEmptyTerminalOutput(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"cmp_pt_1","type":"compaction","status":"completed","encrypted_content":"compact-pt-raw"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_compact_pt_raw","object":"response","status":"completed","output":[],"usage":{"input_tokens":6,"output_tokens":2,"total_tokens":8}}}`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
	}

	result, err := responseupstream.ReadPassthroughNonStreaming(context.Background(), resp, ResponseSink{Writer: c.Writer}, svc.PassthroughOptions(context.Background(), c, nil), "gpt-5.5", "")
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	events := httptestkit.ParseCompactSSE(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "compact-pt-raw", gjson.Get(events[0][1], "item.encrypted_content").String())
	require.Len(t, gjson.Get(events[1][1], "response.output").Array(), 1)
}

// TestHandleNonStreamingResponsePassthrough_CompactClientStreamBridgesToSSE checks Compact bridging in OAuth
// passthrough.
func TestHandleNonStreamingResponsePassthrough_CompactClientStreamBridgesToSSE(t *testing.T) {
	svc := newCompactBridgeTestService()
	c, rec := newCompactBridgeTestContext(t, true)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"resp_compact_pt",
			"output":[{"id":"cmp_pt_1","type":"compaction","encrypted_content":"compact-pt-payload"}],
			"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}
		}`)),
	}

	result, err := responseupstream.ReadPassthroughNonStreaming(context.Background(), resp, ResponseSink{Writer: c.Writer}, svc.PassthroughOptions(context.Background(), c, nil), "gpt-5.5", "")
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	events := httptestkit.ParseCompactSSE(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "resp_compact_pt", gjson.Get(events[1][1], "response.id").String())
	require.NotNil(t, result.Usage)
	require.Equal(t, 7, result.Usage.InputTokens)
}

func (r *passthroughFlushTestErrorBody) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.payload), nil
	}
	return 0, r.err
}

func (r *passthroughFlushTestErrorBody) Close() error { return nil }

func runPassthroughFlushTest(
	t *testing.T,
	body io.ReadCloser,
	failAfterWrites int,
	setups ...func(*gin.Context),
) (*responseupstream.StreamingResult, *httptest.ResponseRecorder, *passthroughFlushTestWriter, error) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	writer := &passthroughFlushTestWriter{
		ResponseWriter:  c.Writer,
		recorder:        recorder,
		failAfterWrites: failAfterWrites,
	}
	c.Writer = writer
	for _, setup := range setups {
		setup(c)
	}

	output := newAuxiliaryFixture(auxiliaryFixtureInputs{}).Output
	output.Options = OpenAIResponseOptions{Configured: true, ReadLimit: 128 * 1024 * 1024, MaxLineSize: OpenAIResponseDefaultMaxLineSize}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
	}
	result, err := responseupstream.ReadPassthroughStreaming(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), output.PassthroughOptions(context.Background(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "flush-test"}}), time.Now(), "", "")
	return result, recorder, writer, err
}

func TestOpenAIStreamingPassthroughFlushesAtCompleteEventBoundaries(t *testing.T) {
	firstEvent := "event: response.output_text.delta\n" +
		"id: event-1\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n"
	heartbeat := ": keepalive\n\n"
	// Provide complete output so the test can check Flush calls.
	terminalEvent := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_flush","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}}` + "\n\n"
	upstream := firstEvent + heartbeat + terminalEvent

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{
		len(firstEvent),
		len(firstEvent) + len(heartbeat),
		len(upstream),
	}, writer.flushBodyLengths)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughKeepsPreamblePendingUntilFirstOutputBoundary(t *testing.T) {
	preamble := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_pending"}}` + "\n\n" +
		": waiting\n\n"
	firstOutput := `data: {"type":"response.output_text.delta","delta":"ready"}` + "\n\n"
	terminalEvent := `data: {"type":"response.completed","response":{"id":"resp_pending","usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ready"}]}]}}` + "\n\n"
	upstream := preamble + firstOutput + terminalEvent

	_, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.NoError(t, err)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{
		len(preamble) + len(firstOutput),
		len(upstream),
	}, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughFlushesTerminalEventAtEOFWithoutBlankLine(t *testing.T) {
	upstream := "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_eof","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7},"output":[]}}`
	wantBody := upstream + "\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, wantBody, recorder.Body.String())
	require.Equal(t, []int{len(wantBody)}, writer.flushBodyLengths)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughFailedBeforeOutputCanStillFailOverWithoutFlush(t *testing.T) {
	upstream := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_failover"}}` + "\n\n" +
		"event: response.failed\n" +
		`data: {"type":"response.failed","error":{"code":"server_error","message":"upstream processing failed"}}` + "\n\n"

	_, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, recorder.Body.String())
	require.Empty(t, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughNonRetryableFailedBeforeOutputFlushesAtBoundary(t *testing.T) {
	upstream := "event: response.failed\n" +
		`data: {"type":"response.failed","error":{"code":"content_policy","message":"request blocked by policy"},"usage":{"input_tokens":6,"output_tokens":0,"total_tokens":6}}` + "\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{len(upstream)}, writer.flushBodyLengths)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughBareErrorTerminatesBeforeDone(t *testing.T) {
	errorEvent := "event: error\n" +
		`data: {"type":"error","error":{"code":"content_policy","message":"request blocked by policy"},"usage":{"input_tokens":6,"output_tokens":0,"total_tokens":6}}` + "\n\n"
	upstream := errorEvent + "data: [DONE]\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	body := recorder.Body.String()
	require.NotContains(t, body, `"type":"error"`)
	require.Equal(t, 1, strings.Count(body, `"type":"response.failed"`))
	require.Contains(t, body, `"status":"failed"`)
	require.NotContains(t, body, "[DONE]")
	require.Equal(t, []int{len(body)}, writer.flushBodyLengths)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughBareErrorDrainsAuthoritativeFailedUsage(t *testing.T) {
	errorEvent := "event: error\n" +
		`data: {"type":"error","error":{"code":"content_policy","message":"request blocked by policy"}}` + "\n\n"
	failedEvent := "event: response.failed\n" +
		`data: {"type":"response.failed","response":{"id":"resp_failed","usage":{"input_tokens":9,"output_tokens":2},"error":{"code":"content_policy","message":"request blocked by policy"}}}` + "\n\n"
	upstream := errorEvent + failedEvent + "data: [DONE]\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	require.NotNil(t, result)
	body := recorder.Body.String()
	require.NotContains(t, body, `"type":"error"`)
	require.Equal(t, 1, strings.Count(body, `"type":"response.failed"`))
	require.Contains(t, body, `"id":"resp_failed"`)
	require.NotContains(t, body, "[DONE]")
	require.Equal(t, []int{len(body)}, writer.flushBodyLengths)
	require.Equal(t, 9, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughFailedAfterOutputFlushesAtBoundaryAndKeepsUsage(t *testing.T) {
	firstOutput := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	failedEvent := "event: response.failed\n" +
		`data: {"type":"response.failed","error":{"code":"server_error","message":"upstream processing failed"},"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}` + "\n\n"
	upstream := firstOutput + failedEvent

	result, recorder, writer, err := runPassthroughFlushTest(t, io.NopCloser(strings.NewReader(upstream)), -1)

	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.NotNil(t, result)
	require.Equal(t, upstream, recorder.Body.String())
	require.Equal(t, []int{len(firstOutput), len(upstream)}, writer.flushBodyLengths)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughClientDisconnectStillDrainsTerminalUsage(t *testing.T) {
	firstOutput := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"
	terminalEvent := `data: {"type":"response.completed","response":{"id":"resp_drain","usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}}}` + "\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(
		t,
		io.NopCloser(strings.NewReader(firstOutput+terminalEvent)),
		2,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, firstOutput, recorder.Body.String())
	require.Equal(t, []int{len(firstOutput)}, writer.flushBodyLengths)
	require.Equal(t, 1, writer.failedWrites)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
}

func TestOpenAIStreamingPassthroughScannerErrorFlushesWrittenResidual(t *testing.T) {
	upstream := []byte(`data: {"type":"response.output_text.delta","delta":"partial"}`)
	readErr := errors.New("upstream read failed")

	_, recorder, writer, err := runPassthroughFlushTest(t, &passthroughFlushTestErrorBody{
		payload: upstream,
		err:     readErr,
	}, -1)

	require.ErrorIs(t, err, readErr)
	wantBody := string(upstream) + "\n"
	require.Equal(t, wantBody, recorder.Body.String())
	require.Equal(t, []int{len(wantBody)}, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughNamespaceRestoreErrorFlushesWrittenResidualOnce(t *testing.T) {
	writtenPrefix := `data: {"type":"response.output_text.delta","delta":"prefix"}` + "\n"
	overflowData := `data: {"type":"response.output_text.delta","delta":"not-written","overflow":1e1000}`

	_, recorder, writer, err := runPassthroughFlushTest(
		t,
		io.NopCloser(strings.NewReader(writtenPrefix+overflowData)),
		-1,
		func(c *gin.Context) {
			SetOpenAIResponsesNamespaceNames(c, map[string]bridge.ResponsesNamespaceName{
				"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
			})
		},
	)

	require.ErrorContains(t, err, "restore OpenAI passthrough namespace response")
	require.Equal(t, writtenPrefix, recorder.Body.String())
	require.Equal(t, []int{len(writtenPrefix)}, writer.flushBodyLengths)
}

func TestOpenAIStreamingPassthroughBlankWriteFailureDoesNotFlushAndStillDrainsUsage(t *testing.T) {
	writtenDataLine := `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n"
	terminalEvent := `data: {"type":"response.completed","response":{"id":"resp_blank_failure","usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}}` + "\n\n"

	result, recorder, writer, err := runPassthroughFlushTest(
		t,
		io.NopCloser(strings.NewReader(writtenDataLine+"\n"+terminalEvent)),
		1,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, writtenDataLine, recorder.Body.String())
	require.Empty(t, writer.flushBodyLengths)
	require.Equal(t, 1, writer.successfulWrites)
	require.Equal(t, 1, writer.failedWrites)
	require.Equal(t, 13, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestHandleStreamingResponsePassthroughDeduplicatesFunctionCallArguments(t *testing.T) {
	argsA := `{"cmd":"echo hi","meta":{"nested":[1,{"ok":true}],"quote":"a}b"}}`
	argsB := `{"path":"/tmp/file","patch":{"ops":[{"op":"replace","value":{"lines":["x","y"]}}]}}`
	upstreamBody := strings.Join([]string{
		passthroughArgsSSEData(`{"type":"response.created","response":{"id":"resp_passthrough_args","model":"gpt-5.4"}}`),
		passthroughArgsSSEData(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_a","call_id":"call_a","name":"exec_command","arguments":"","status":"in_progress"}}`),
		passthroughArgsSSEData(functionArgsDeltaJSON(0, "fc_a", "call_a", "exec_command", `{"cmd":`)),
		passthroughArgsSSEData(functionArgsDeltaJSON(0, "fc_a", "call_a", "exec_command", `"echo hi","meta":{"nested":[1,{"ok":true}],"quote":"a}b"}}`)),
		passthroughArgsSSEData(functionArgsDoneJSON(0, "fc_a", "call_a", "exec_command", argsA+argsA)),
		passthroughArgsSSEData(outputItemDoneJSON(0, "fc_a", "call_a", "exec_command", argsA+argsA)),
		passthroughArgsSSEData(`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_b","call_id":"call_b","name":"apply_patch","arguments":"","status":"in_progress"}}`),
		passthroughArgsSSEData(functionArgsDeltaJSON(1, "fc_b", "call_b", "apply_patch", `{"path":"/tmp/file",`)),
		passthroughArgsSSEData(functionArgsDeltaJSON(1, "fc_b", "call_b", "apply_patch", `"patch":{"ops":[{"op":"replace","value":{"lines":["x","y"]}}]}}`)),
		passthroughArgsSSEData(functionArgsDoneJSON(1, "fc_b", "call_b", "apply_patch", argsB+argsB)),
		passthroughArgsSSEData(outputItemDoneJSON(1, "fc_b", "call_b", "apply_patch", argsB+argsB)),
		passthroughArgsSSEData(completedWithFunctionCallsJSON(argsA+argsA, argsB+argsB)),
		"data: [DONE]\n\n",
	}, "")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}

	svc := newResponsesFixture(responsesFixtureInputs{})
	result, err := responseupstream.ReadPassthroughStreaming(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(context.Background(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}), time.Now(), "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, result)

	events := collectPassthroughArgsSSEDataPayloads(t, rec.Body.String())
	require.Equal(t, argsA, accumulateFunctionArgumentDeltas(events, "call_a"))
	require.Equal(t, argsB, accumulateFunctionArgumentDeltas(events, "call_b"))

	require.Equal(t, argsA, gjson.Get(findPassthroughArgsSSEEvent(t, events, "response.function_call_arguments.done", "call_a"), "arguments").String())
	require.Equal(t, argsB, gjson.Get(findPassthroughArgsSSEEvent(t, events, "response.function_call_arguments.done", "call_b"), "arguments").String())
	require.Equal(t, argsA, gjson.Get(findPassthroughArgsSSEEvent(t, events, "response.output_item.done", "call_a"), "item.arguments").String())
	require.Equal(t, argsB, gjson.Get(findPassthroughArgsSSEEvent(t, events, "response.output_item.done", "call_b"), "item.arguments").String())

	completed := findPassthroughArgsSSEEvent(t, events, "response.completed", "")
	require.Equal(t, argsA, gjson.Get(completed, "response.output.0.arguments").String())
	require.Equal(t, argsB, gjson.Get(completed, "response.output.1.arguments").String())
	requireJSONArgument(t, gjson.Get(completed, "response.output.0.arguments").String())
	requireJSONArgument(t, gjson.Get(completed, "response.output.1.arguments").String())
}

func functionArgsDeltaJSON(outputIndex int, itemID, callID, name, delta string) string {
	return fmt.Sprintf(
		`{"type":"response.function_call_arguments.delta","output_index":%d,"item_id":%s,"call_id":%s,"name":%s,"delta":%s}`,
		outputIndex,
		strconv.Quote(itemID),
		strconv.Quote(callID),
		strconv.Quote(name),
		strconv.Quote(delta),
	)
}

func functionArgsDoneJSON(outputIndex int, itemID, callID, name, arguments string) string {
	return fmt.Sprintf(
		`{"type":"response.function_call_arguments.done","output_index":%d,"item_id":%s,"call_id":%s,"name":%s,"arguments":%s}`,
		outputIndex,
		strconv.Quote(itemID),
		strconv.Quote(callID),
		strconv.Quote(name),
		strconv.Quote(arguments),
	)
}

func outputItemDoneJSON(outputIndex int, itemID, callID, name, arguments string) string {
	return fmt.Sprintf(
		`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":%s,"call_id":%s,"name":%s,"arguments":%s,"status":"completed"}}`,
		outputIndex,
		strconv.Quote(itemID),
		strconv.Quote(callID),
		strconv.Quote(name),
		strconv.Quote(arguments),
	)
}

func completedWithFunctionCallsJSON(argsA, argsB string) string {
	return fmt.Sprintf(
		`{"type":"response.completed","response":{"id":"resp_passthrough_args","status":"completed","output":[{"type":"function_call","id":"fc_a","call_id":"call_a","name":"exec_command","arguments":%s,"status":"completed"},{"type":"function_call","id":"fc_b","call_id":"call_b","name":"apply_patch","arguments":%s,"status":"completed"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		strconv.Quote(argsA),
		strconv.Quote(argsB),
	)
}

func requireJSONArgument(t *testing.T, arguments string) {
	t.Helper()
	var decoded any
	require.NoError(t, json.Unmarshal([]byte(arguments), &decoded))
}

func TestOpenAIStreamingPassthroughMissingTerminalEventReturnsIncompleteError(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\",\"output_index\":0}\n\n"))
	}()

	_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}), time.Now(), "", "")
	_ = pr.Close()
	if err == nil || !strings.Contains(err.Error(), "missing terminal event") {
		t.Fatalf("expected missing terminal event error, got %v", err)
	}
}

func TestOpenAIStreamingPassthroughPostOutputDisconnectQuarantinesSharedProxy(t *testing.T) {
	proxyID := int64(4698)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 469804, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, ProxyID: &proxyID}}
	svc := newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}})
	// Disable concurrent-truncation coalescing so the two loops count as independent failures.
	svc.Output.ProxyCircuit = egress.NewProxyStreamCircuit(egress.ProxyStreamCircuitSettings{
		FailureThreshold: 2,
		FailureWindow:    time.Minute,
		QuarantineTTL:    10 * time.Minute,
		MaxEntries:       16,
	})

	for _, readErr := range []error{io.ErrUnexpectedEOF, errors.New("http2: client connection lost")} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body: &openAIStreamReadThenErrorCloser{
				reader: strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"),
				err:    readErr,
			},
			Header: http.Header{"X-Request-Id": []string{"rid-passthrough-proxy-disconnect"}},
		}

		_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, provider), time.Now(), "model", "model")
		require.Error(t, err)
		var failoverErr *forwardcore.UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr), "post-output disconnect must not fail over inside the same stream")
		require.Contains(t, rec.Body.String(), "partial")
	}

	require.True(t, svc.Output.ProxyCircuit.IsBlocked(*provider.Record.ProxyID, time.Now()))
}

func TestOpenAIStreamingPassthroughResponseFailedBeforeOutputReturnsFailover(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","error":{"message":"upstream processing failed"}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-passthrough-failed"}},
	}

	_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}), time.Now(), "", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "upstream processing failed")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIStreamingPassthroughContextWindowResponseFailedBeforeOutputAppliesPassthroughRule(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	rule := gatewaytestkit.NonFailoverRule(http.StatusBadRequest, "input exceeds the context window", http.StatusBadRequest, "")
	rule.Platforms = []string{capability.PlatformOpenAI}
	rule.PassthroughBody = true
	rule.CustomMessage = nil
	ruleSvc := gatewaytestkit.ErrorRules([]*errorpolicy.ErrorPassthroughRule{rule})
	BindErrorPassthroughService(c, ruleSvc)

	upstreamMessage := "Your input exceeds the context window of this model. Please adjust your input and try again."
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"` + upstreamMessage + `"}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-pass-context-window-passthrough-rule"}},
	}

	_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}), time.Now(), "", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, IsResponseCommitted(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	require.Equal(t, "upstream_error", gjson.Get(body, "error.type").String())
	require.Equal(t, upstreamMessage, gjson.Get(body, "error.message").String())
	require.NotContains(t, body, "response.failed")
	require.NotContains(t, body, "Upstream request failed")
	// A matched passthrough rule still records an Ops upstream error event.
	opsVal, opsRecorded := c.Get(OpsUpstreamErrorsKey)
	require.True(t, opsRecorded, "passthrough hit should record an ops upstream error event")
	opsEvents, _ := opsVal.([]*ops.OpsUpstreamErrorEvent)
	require.NotEmpty(t, opsEvents)
}

func TestOpenAIStreamingPassthroughContextWindowResponseFailedBeforeOutputWithoutRulePassesThrough(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-pass-context-window-no-rule"}},
	}

	_, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}), time.Now(), "", "")
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	body := rec.Body.String()
	require.Contains(t, body, "event: response.failed")
	require.Contains(t, body, "context_length_exceeded")
	require.Contains(t, body, "Your input exceeds the context window")
}

func TestOpenAIStreamingPassthroughResponseFailedAfterOutputSanitizesVerboseResponseForClient(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	longInstructions := strings.Repeat("You are GPT-5.1 running in the Codex CLI. ", 20)
	failedPayload := fmt.Sprintf(
		`{"type":"response.failed","response":{"id":"resp_pass_failed","object":"response","created_at":1782446336,"status":"failed","instructions":%q,"output":[{"type":"message","content":[{"type":"output_text","text":"large"}]}],"usage":{"input_tokens":123,"output_tokens":0},"error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`,
		longInstructions,
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_pass_failed"}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","delta":"partial"}`,
			"",
			"event: response.failed",
			"data: " + failedPayload,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-pass-failed-after-output"}},
	}

	result, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1, Platform: capability.PlatformOpenAI, Name: "acc"}}), time.Now(), "", "")
	require.Error(t, err)
	require.NotNil(t, result)
	require.Equal(t, 123, result.Usage.InputTokens)

	body := rec.Body.String()
	require.Contains(t, body, "event: response.failed")
	require.Contains(t, body, "context_length_exceeded")
	require.Contains(t, body, `"type":"invalid_request_error"`)
	require.Contains(t, body, "Your input exceeds the context window")
	require.NotContains(t, body, "You are GPT-5.1 running in the Codex CLI")
	require.NotContains(t, body, `"instructions"`)
	require.NotContains(t, body, `"output"`)
	require.NotContains(t, body, `"usage"`)
}

func TestOpenAIStreamingPassthroughResponseDoneWithoutDoneMarkerStillSucceeds(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.done\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":1}}}}\n\n"))
	}()

	result, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}), time.Now(), "", "")
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
}

func TestOpenAIStreamingPassthroughResponseIncompleteWithoutDoneMarkerStillSucceeds(t *testing.T) {
	cfg := &responsesFixtureOptions{Response: OpenAIResponseOptions{MaxLineSize: OpenAIResponseDefaultMaxLineSize}}
	svc := newResponsesFixture(responsesFixtureInputs{options: cfg})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":1}}}}\n\n"))
	}()

	result, err := responseupstream.ReadPassthroughStreaming(c.Request.Context(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(c.Request.Context(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}), time.Now(), "", "")
	_ = pr.Close()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Usage)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
}

func TestOpenAIGatewayServiceHandleResponsesImageOutputs_StreamingPassthrough(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&auxiliaryHTTPRecorder{})
	c, recorder := newOpenAIImageGenerationControlTestContext(true, "unit-test-agent/1.0")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"ig_stream_1\",\"type\":\"image_generation_call\",\"status\":\"in_progress\",\"result\":\"final-image\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image_stream\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"ig_stream_1\",\"type\":\"image_generation_call\",\"status\":\"in_progress\",\"result\":\"final-image\"}],\"usage\":{\"input_tokens\":11,\"output_tokens\":5,\"output_tokens_details\":{\"image_tokens\":4}}}}\n\n",
		)),
	}

	result, err := responseupstream.ReadPassthroughStreaming(context.Background(), resp, upstreamcore.NewOutputContext(ResponseSink{Writer: c.Writer}), svc.Output.PassthroughOptions(context.Background(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 1}}), time.Now(), "gpt-5.5", "gpt-5.5")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, recorder.Body.String(), `"status":"in_progress"`)
	require.Equal(t, 2, strings.Count(recorder.Body.String(), `"status":"completed"`))
}

// TestNonStreamingPassthroughSSEToJSON_CapacityFailedEventFailsOver checks failover on passthrough capacity
// failure events.
func TestNonStreamingPassthroughSSEToJSON_CapacityFailedEventFailsOver(t *testing.T) {
	c, rec := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	body := sseTerminalBody("response.failed",
		`{"type":"response.failed","error":{"message":"Selected model is at capacity. Please try a different model.","type":"invalid_request_error"}}`)

	result, err := responseupstream.ReadPassthroughSSEAsJSON(newNonStreamingSSEResponse(), ResponseSink{Writer: c.Writer}, svc.PassthroughOptions(c.Request.Context(), c, newNonStreamingFailoverProvider()), body, "model", "model")

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Contains(t, string(failoverErr.ResponseBody), "Selected model is at capacity")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestOpenAIGatewayService_OAuthPassthrough_NamespaceNonStreamingResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_1","output":[{"type":"function_call","name":"collaboration__spawn_agent","call_id":"call_1","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}
	names := map[string]bridge.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	}
	SetOpenAIResponsesNamespaceNames(c, names)

	result, err := responseupstream.ReadPassthroughNonStreaming(context.Background(), resp, ResponseSink{Writer: c.Writer}, newResponsesFixture(responsesFixtureInputs{options: &responsesFixtureOptions{}}).Output.PassthroughOptions(context.Background(), c, &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 91}}), "gpt-5.5", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotContains(t, rec.Body.String(), "collaboration__spawn_agent")
	require.Contains(t, rec.Body.String(), `"name":"spawn_agent"`)
	require.Contains(t, rec.Body.String(), `"namespace":"collaboration"`)
}

func TestOpenAIStreamingPassthroughRepairsConcatenatedJSONDocumentsInSingleDataLine(t *testing.T) {
	testOpenAIStreamingRepairsConcatenatedJSONDocuments(t, true, 0)
}
