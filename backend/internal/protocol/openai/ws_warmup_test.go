package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestWSWarmupPayloadAndEvents distinguishes boolean-false warmup from missing,
// nonboolean, wrong-type, and trailing-data requests. Its event pair must share
// ID/model/time, carry ordered sequence numbers and empty output, and finish
// with completed status and zero total-token usage.
func TestWSWarmupPayloadAndEvents(t *testing.T) {
	require.True(t, IsWSWarmupPayload([]byte(`{"type":"response.create","generate":false}`)))
	for _, body := range []string{`{"type":"response.create"}`, `{"type":"response.create","generate":true}`, `{"type":"response.create","generate":"false"}`, `{"type":"response.create","generate":null}`, `{"type":"session.update","generate":false}`, `{"type":"response.create","generate":false} trailing`} {
		require.False(t, IsWSWarmupPayload([]byte(body)), body)
	}
	events, err := WSWarmupEvents("resp_warm", "client-model", 123)
	require.NoError(t, err)
	require.Len(t, events, 2)
	for i, event := range events {
		require.True(t, gjson.GetBytes(event, "sequence_number").Exists())
		require.Equal(t, int64(i), gjson.GetBytes(event, "sequence_number").Int())
		require.Equal(t, "resp_warm", gjson.GetBytes(event, "response.id").String())
		require.Equal(t, "client-model", gjson.GetBytes(event, "response.model").String())
		require.Equal(t, int64(123), gjson.GetBytes(event, "response.created_at").Int())
		require.Equal(t, "[]", gjson.GetBytes(event, "response.output").Raw)
	}
	require.Equal(t, "in_progress", gjson.GetBytes(events[0], "response.status").String())
	require.Equal(t, "completed", gjson.GetBytes(events[1], "response.status").String())
	require.Zero(t, gjson.GetBytes(events[1], "response.usage.total_tokens").Int())
}
