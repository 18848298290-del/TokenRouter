package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	httpapitestkit "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi/testkit"
)

// Unary Compact output becomes ordered Responses events for marked streaming
// clients. These cases check successful item and terminal framing, failure after
// heartbeat commitment, generated IDs, malformed usage removal, and the failed
// response object's timestamp and status fields.

// TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitAppendsEvents waits for
// comments before a successful Compact result. After stripping comments, output
// must contain one item event and the completed response with its upstream ID.
func TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitAppendsEvents(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	// Allow comment framing before the result arrives. The bridge must stop
	// the controller before appending item and terminal events to that existing stream.
	waitForKeepaliveBeats()

	finalResponse := []byte(`{"id":"resp_ka_1","output":[{"id":"cmp_ka","type":"compaction","encrypted_content":"x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	require.True(t, WriteOpenAICompactSSEBridge(c, http.StatusOK, finalResponse, MarkOpsStreamError))

	require.Equal(t, http.StatusOK, rec.Code)
	events := httpapitestkit.ParseCompactSSE(t, stripKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 2)
	require.Equal(t, "response.output_item.done", events[0][0])
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "response.completed", events[1][0])
	require.Equal(t, "resp_ka_1", gjson.Get(events[1][1], "response.id").String())
}

// TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitFailureEmitsFailedEvent checks response.failed and Ops
// observation after HTTP 200.
// The terminal object must include a failure message and generated ID, while
// the saved Ops error retains the intended bad-gateway status for monitoring.
func TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitFailureEmitsFailedEvent(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	// Let heartbeat setup choose the stream first, so failure must be observed
	// through an appended terminal event while Ops retains the intended error status.
	waitForKeepaliveBeats()

	require.True(t, WriteOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"upstream exploded"}}`), MarkOpsStreamError))

	events := httpapitestkit.ParseCompactSSE(t, stripKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 1)
	require.Equal(t, "response.failed", events[0][0])
	require.Equal(t, "failed", gjson.Get(events[0][1], "response.status").String())
	require.Contains(t, gjson.Get(events[0][1], "response.error.message").String(), "upstream exploded")
	require.NotEmpty(t, gjson.Get(events[0][1], "response.id").String())

	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
}

// TestWriteOpenAICompactSSEBridge_BeforeKeepaliveCommitFailureKeepsJSONPath checks that uncommitted failures leave
// JSON output to the caller.
// A bad-gateway result must return false with an empty recorder after the
// controller stops before its first beat, leaving response construction unclaimed.
func TestWriteOpenAICompactSSEBridge_BeforeKeepaliveCommitFailureKeepsJSONPath(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// Delay the first heartbeat and stop immediately so the bridge receives
	// an uncommitted writer and must leave the bad-gateway result for ordinary HTTP handling.
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()

	require.False(t, WriteOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"fast fail"}}`), MarkOpsStreamError))
	require.Zero(t, rec.Body.Len())
}

// TestBuildOpenAICompactSSEPayload_EmitsItemsAndCompleted converts a response with
// compaction and message output. Item events must retain their asserted fields
// and indexes, followed by a completed event carrying the response ID, usage,
// and both output objects.
func TestBuildOpenAICompactSSEPayload_EmitsItemsAndCompleted(t *testing.T) {
	finalResponse := []byte(`{
		"id":"resp_compact_1",
		"object":"response",
		"model":"gpt-5.1-codex",
		"status":"completed",
		"output":[
			{"id":"cmp_1","type":"compaction","status":"completed","encrypted_content":"compact-payload","summary":[{"type":"summary_text","text":"compact summary"}],"opaque":{"kept":true}},
			{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
		],
		"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}
	}`)

	payload, ok := BuildOpenAICompactSSEPayload(finalResponse)
	require.True(t, ok)

	events := httpapitestkit.ParseCompactSSE(t, string(payload))
	require.Len(t, events, 3)

	require.Equal(t, "response.output_item.done", events[0][0])
	first := events[0][1]
	require.Equal(t, "response.output_item.done", gjson.Get(first, "type").String())
	require.Equal(t, int64(0), gjson.Get(first, "output_index").Int())
	require.Equal(t, "compaction", gjson.Get(first, "item.type").String())
	require.Equal(t, "cmp_1", gjson.Get(first, "item.id").String())
	require.Equal(t, "compact-payload", gjson.Get(first, "item.encrypted_content").String())
	require.Equal(t, "compact summary", gjson.Get(first, "item.summary.0.text").String())
	require.True(t, gjson.Get(first, "item.opaque.kept").Bool(), "item 原始字段必须逐字节保留")

	require.Equal(t, "response.output_item.done", events[1][0])
	require.Equal(t, int64(1), gjson.Get(events[1][1], "output_index").Int())
	require.Equal(t, "message", gjson.Get(events[1][1], "item.type").String())

	require.Equal(t, "response.completed", events[2][0])
	completed := events[2][1]
	require.Equal(t, "response.completed", gjson.Get(completed, "type").String())
	require.Equal(t, "resp_compact_1", gjson.Get(completed, "response.id").String())
	require.Equal(t, int64(13), gjson.Get(completed, "response.usage.total_tokens").Int())
	require.Len(t, gjson.Get(completed, "response.output").Array(), 2)
}

// TestBuildOpenAICompactSSEPayload_InjectsMissingResponseID supplies output with no
// response ID. Conversion must emit item and completed events, with the
// completed response carrying a nonempty generated identifier after the resp_ prefix.
func TestBuildOpenAICompactSSEPayload_InjectsMissingResponseID(t *testing.T) {
	payload, ok := BuildOpenAICompactSSEPayload([]byte(`{"output":[{"type":"compaction","encrypted_content":"x"}]}`))
	require.True(t, ok)

	events := httpapitestkit.ParseCompactSSE(t, string(payload))
	require.Len(t, events, 2)
	completed := events[1][1]
	// Checking the prefix alone could accept an empty generated ID. Require a
	// nonempty suffix for the completed response before accepting its identifier.
	id := gjson.Get(completed, "response.id").String()
	require.True(t, strings.HasPrefix(id, "resp_"), "缺失 id 必须注入 resp_* 兜底: %q", id)
	require.NotEqual(t, "resp_", id)
}

// TestBuildOpenAICompactSSEPayload_ReplacesNonStringResponseID supplies a numeric
// response ID in otherwise usable output. Conversion must replace that value
// with a string carrying the Responses prefix in the completed event.
func TestBuildOpenAICompactSSEPayload_ReplacesNonStringResponseID(t *testing.T) {
	payload, ok := BuildOpenAICompactSSEPayload([]byte(`{"id":123,"output":[{"type":"compaction","encrypted_content":"x"}]}`))
	require.True(t, ok)

	events := httpapitestkit.ParseCompactSSE(t, string(payload))
	id := gjson.Get(events[len(events)-1][1], "response.id")
	require.Equal(t, gjson.String, id.Type)
	require.True(t, strings.HasPrefix(id.String(), "resp_"))
}

// TestBuildOpenAICompactSSEPayload_DropsMalformedUsage varies incomplete counts,
// fractions, negatives, exponent notation, and string values. Conversion must
// still complete successfully while omitting those unusable usage objects from the terminal response.
func TestBuildOpenAICompactSSEPayload_DropsMalformedUsage(t *testing.T) {
	for name, usage := range map[string]string{
		"缺少字段": `{"prompt_tokens":9,"completion_tokens":4}`,
		"小数":   `{"input_tokens":9.5,"output_tokens":4,"total_tokens":13}`,
		"负数":   `{"input_tokens":-1,"output_tokens":4,"total_tokens":3}`,
		"指数":   `{"input_tokens":1e3,"output_tokens":4,"total_tokens":1004}`,
		"字符串":  `{"input_tokens":"9","output_tokens":4,"total_tokens":13}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"id":"resp_1","output":[{"type":"compaction","encrypted_content":"x"}],"usage":` + usage + `}`)
			payload, ok := BuildOpenAICompactSSEPayload(body)
			require.True(t, ok)

			events := httpapitestkit.ParseCompactSSE(t, string(payload))
			completed := events[len(events)-1][1]
			// Keep the terminal response available when its usage violates unsigned
			// token-count spelling; the converter removes that object before the event is emitted.
			require.False(t, gjson.Get(completed, "response.usage").Exists())
		})
	}
}

// TestBuildOpenAICompactSSEPayload_KeepsWellFormedUsage supplies unsigned token
// counts with nested cached-token details. The completed response must keep
// its asserted input and cached counts after unary-to-stream conversion.
func TestBuildOpenAICompactSSEPayload_KeepsWellFormedUsage(t *testing.T) {
	payload, ok := BuildOpenAICompactSSEPayload([]byte(`{
		"id":"resp_1",
		"output":[{"type":"compaction","encrypted_content":"x"}],
		"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13,"input_tokens_details":{"cached_tokens":2}}
	}`))
	require.True(t, ok)

	events := httpapitestkit.ParseCompactSSE(t, string(payload))
	completed := events[len(events)-1][1]
	require.Equal(t, int64(9), gjson.Get(completed, "response.usage.input_tokens").Int())
	require.Equal(t, int64(2), gjson.Get(completed, "response.usage.input_tokens_details.cached_tokens").Int())
}

// TestBuildOpenAICompactSSEPayload_RejectsNonJSONObject supplies empty, SSE, array,
// text, and boolean bodies. None may be claimed as a Compact JSON object,
// so conversion must return false for each supplied representation.
func TestBuildOpenAICompactSSEPayload_RejectsNonJSONObject(t *testing.T) {
	for name, body := range map[string][]byte{
		"empty":     nil,
		"sse_text":  []byte("data: {\"type\":\"response.completed\"}\n\n"),
		"array":     []byte(`[{"id":"resp_1"}]`),
		"non_json":  []byte("upstream said no"),
		"bare_true": []byte("true"),
	} {
		_, ok := BuildOpenAICompactSSEPayload(body)
		require.False(t, ok, "case %s 不应被合成为 SSE", name)
	}
}

// TestWriteOpenAICompactSSEBridge_RequiresMarkAndSuccessStatus compares unmarked
// output, a marked uncommitted error, and marked success. The first two
// cases must remain unclaimed with no bytes, while success selects SSE and emits completion.
func TestWriteOpenAICompactSSEBridge_RequiresMarkAndSuccessStatus(t *testing.T) {
	finalResponse := []byte(`{"id":"resp_1","output":[{"type":"compaction","encrypted_content":"x"}]}`)

	// An unmarked client has not delegated output to this bridge. Returning
	// false and writing no bytes lets the caller continue its own response construction.
	c, rec := newCompactBridgeTestContext(t, false)
	require.False(t, WriteOpenAICompactSSEBridge(c, http.StatusOK, finalResponse, MarkOpsStreamError))
	require.Zero(t, rec.Body.Len())

	// Keep the writer uncommitted for a non-2xx result. The bridge must leave
	// output unclaimed so the caller can select the corresponding HTTP error status.
	c, rec = newCompactBridgeTestContext(t, true)
	require.False(t, WriteOpenAICompactSSEBridge(c, http.StatusBadGateway, finalResponse, MarkOpsStreamError))
	require.Zero(t, rec.Body.Len())

	// A marked 2xx object permits bridge takeover. Its completion event and SSE
	// header identify the selected client framing after the earlier unclaimed cases.
	c, rec = newCompactBridgeTestContext(t, true)
	require.True(t, WriteOpenAICompactSSEBridge(c, http.StatusOK, finalResponse, MarkOpsStreamError))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "event: response.completed")
}

// TestWriteOpenAICompactSSEFailureMessage_CarriesCreatedAt decodes the generated
// failure event into its expected response shape. The event and response
// kinds must identify failure, and created_at must contain a positive Unix timestamp.
func TestWriteOpenAICompactSSEFailureMessage_CarriesCreatedAt(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	WriteOpenAICompactSSEFailureMessage(c, http.StatusBadGateway, "upstream_error", "boom", MarkOpsStreamError)

	body := rec.Body.String()
	require.Contains(t, body, "event: response.failed")

	_, payload, found := strings.Cut(body, "data: ")
	require.True(t, found, "SSE 帧必须带 data 行: %q", body)

	var event struct {
		Type     string `json:"type"` // terminal event kind
		Response struct {
			ID        string `json:"id"`         // generated response identifier
			Object    string `json:"object"`     // expected response kind
			CreatedAt int64  `json:"created_at"` // Unix seconds
			Status    string `json:"status"`     // terminal failed status
		} `json:"response"` // decoded terminal response
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(payload)), &event))

	require.Equal(t, "response.failed", event.Type)
	require.Equal(t, "response", event.Response.Object)
	require.Equal(t, "failed", event.Response.Status)
	require.Greater(t, event.Response.CreatedAt, int64(0),
		"response.failed 必须带有效的 created_at，否则严格客户端读不出这帧")
}
