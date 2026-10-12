package bridge

import (
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// collectStreamEvents converts decoded Chat chunks using a fresh state with
// the deepseek-v4-pro model. Invalid fixtures fail the test, and finalization
// events are appended after all supplied chunks.
func collectStreamEvents(t *testing.T, chunks []string) []ResponsesStreamEvent {
	t.Helper()
	state := NewChatCompletionsToResponsesStreamState(testRuntime(), "deepseek-v4-pro")
	var events []ResponsesStreamEvent
	for _, payload := range chunks {
		var chunk ChatCompletionsChunk
		require.NoError(t, json.Unmarshal([]byte(payload), &chunk))
		events = append(events, ChatCompletionsChunkToResponsesEvents(testRuntime(), &chunk, state)...)
	}
	events = append(events, FinalizeChatCompletionsResponsesStream(testRuntime(), state)...)
	return events
}

// responseObjectOf marshals an event and requires its response field to be an
// object. It returns that decoded map for assertions and fails the test for
// any other field shape.
func responseObjectOf(t *testing.T, evt ResponsesStreamEvent) map[string]any {
	t.Helper()
	m := marshalEvent(t, evt)
	resp, ok := m["response"].(map[string]any)
	require.True(t, ok, "event must carry a response object: %v", m)
	return resp
}

// requireCreatedAt requires a numeric creation timestamp and returns its
// integer conversion. The key must be present and the converted Unix-second
// value must be positive, or the test fails.
func requireCreatedAt(t *testing.T, resp map[string]any) int64 {
	t.Helper()
	raw, ok := resp["created_at"]
	require.True(t, ok, "response 对象必须带 created_at，否则严格客户端直接反序列化失败")
	value, ok := raw.(float64)
	require.True(t, ok, "created_at 必须是数字，得到 %T", raw)
	// Compare generated timestamps as integer seconds after generic JSON decoding.
	require.Greater(t, int64(value), int64(0), "created_at 必须是有效的 unix 时间戳")
	return int64(value)
}

// marshalEvent marshals the custom Responses event codec and decodes the
// object for assertions. Either JSON failure stops the test, so callers
// receive a map from a successful round trip.
func marshalEvent(t *testing.T, e ResponsesStreamEvent) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// strPtr returns a fresh string pointer for stream fixture fields.
func strPtr(s string) *string { return &s }

// testRuntime supplies the wall clock and cryptographic random reader for
// conversion tests.
func testRuntime() Runtime { return Runtime{Now: time.Now, ReadRandom: rand.Read} }
