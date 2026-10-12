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

// TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitAppendsEvents checks that committed streams append success
// events.
func TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitAppendsEvents(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
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
func TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitFailureEmitsFailedEvent(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
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
func TestWriteOpenAICompactSSEBridge_BeforeKeepaliveCommitFailureKeepsJSONPath(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()

	require.False(t, WriteOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"fast fail"}}`), MarkOpsStreamError))
	require.Zero(t, rec.Body.Len())
}

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

func TestBuildOpenAICompactSSEPayload_InjectsMissingResponseID(t *testing.T) {
	payload, ok := BuildOpenAICompactSSEPayload([]byte(`{"output":[{"type":"compaction","encrypted_content":"x"}]}`))
	require.True(t, ok)

	events := httpapitestkit.ParseCompactSSE(t, string(payload))
	require.Len(t, events, 2)
	completed := events[1][1]
	// A generated nonempty response.id lets Codex parse ResponseCompleted.
	id := gjson.Get(completed, "response.id").String()
	require.True(t, strings.HasPrefix(id, "resp_"), "缺失 id 必须注入 resp_* 兜底: %q", id)
	require.NotEqual(t, "resp_", id)
}

func TestBuildOpenAICompactSSEPayload_ReplacesNonStringResponseID(t *testing.T) {
	payload, ok := BuildOpenAICompactSSEPayload([]byte(`{"id":123,"output":[{"type":"compaction","encrypted_content":"x"}]}`))
	require.True(t, ok)

	events := httpapitestkit.ParseCompactSSE(t, string(payload))
	id := gjson.Get(events[len(events)-1][1], "response.id")
	require.Equal(t, gjson.String, id.Type)
	require.True(t, strings.HasPrefix(id.String(), "resp_"))
}

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
			// Remove usage that cannot decode into integer token fields, allowing the completed event to parse.
			require.False(t, gjson.Get(completed, "response.usage").Exists())
		})
	}
}

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

func TestWriteOpenAICompactSSEBridge_RequiresMarkAndSuccessStatus(t *testing.T) {
	finalResponse := []byte(`{"id":"resp_1","output":[{"type":"compaction","encrypted_content":"x"}]}`)

	// Unmarked clients keep JSON output.
	c, rec := newCompactBridgeTestContext(t, false)
	require.False(t, WriteOpenAICompactSSEBridge(c, http.StatusOK, finalResponse, MarkOpsStreamError))
	require.Zero(t, rec.Body.Len())

	// Before commitment, non-2xx responses keep HTTP status handling for client retry decisions.
	c, rec = newCompactBridgeTestContext(t, true)
	require.False(t, WriteOpenAICompactSSEBridge(c, http.StatusBadGateway, finalResponse, MarkOpsStreamError))
	require.Zero(t, rec.Body.Len())

	// A marked 2xx response is converted to SSE.
	c, rec = newCompactBridgeTestContext(t, true)
	require.True(t, WriteOpenAICompactSSEBridge(c, http.StatusOK, finalResponse, MarkOpsStreamError))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "event: response.completed")
}

// TestWriteOpenAICompactSSEFailureMessage_CarriesCreatedAt checks the required timestamp in failure events (#5601).
// Strict Responses clients reject a missing created_at field and cannot recognize the terminal event.
// An unrecognized terminal event can cause a stream-closed retry.
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
		Type     string `json:"type"`
		Response struct {
			ID        string `json:"id"`
			Object    string `json:"object"`
			CreatedAt int64  `json:"created_at"`
			Status    string `json:"status"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(payload)), &event))

	require.Equal(t, "response.failed", event.Type)
	require.Equal(t, "response", event.Response.Object)
	require.Equal(t, "failed", event.Response.Status)
	require.Greater(t, event.Response.CreatedAt, int64(0),
		"response.failed 必须带有效的 created_at，否则严格客户端读不出这帧")
}
