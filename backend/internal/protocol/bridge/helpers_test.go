package bridge

import (
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// collectStreamEvents converts Chat Completions chunks and includes stream finalization events.
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

// responseObjectOf extracts the response object from a serialized event.
func responseObjectOf(t *testing.T, evt ResponsesStreamEvent) map[string]any {
	t.Helper()
	m := marshalEvent(t, evt)
	resp, ok := m["response"].(map[string]any)
	require.True(t, ok, "event must carry a response object: %v", m)
	return resp
}

// requireCreatedAt asserts and returns a positive Unix creation timestamp.
func requireCreatedAt(t *testing.T, resp map[string]any) int64 {
	t.Helper()
	raw, ok := resp["created_at"]
	require.True(t, ok, "response 对象必须带 created_at，否则严格客户端直接反序列化失败")
	value, ok := raw.(float64)
	require.True(t, ok, "created_at 必须是数字，得到 %T", raw)
	require.Greater(t, int64(value), int64(0), "created_at 必须是有效的 unix 时间戳")
	return int64(value)
}

// marshalEvent exercises custom JSON serialization and returns the decoded object.
func marshalEvent(t *testing.T, e ResponsesStreamEvent) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func strPtr(s string) *string { return &s }

// testRuntime supplies the clock and random source for conversion tests.
func testRuntime() Runtime { return Runtime{Now: time.Now, ReadRandom: rand.Read} }
