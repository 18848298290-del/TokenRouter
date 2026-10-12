package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// benchmarkOpenAIWSBytesSink retains model-replacement benchmark output.
var benchmarkOpenAIWSBytesSink []byte

func TestOpenAIWSPayloadString_OnlyAcceptsStringValues(t *testing.T) {
	payload := map[string]any{
		"type":                 nil,
		"model":                123,
		"prompt_cache_key":     " cache-key ",
		"previous_response_id": []byte(" resp_1 "),
	}

	require.Equal(t, "", WSPayloadString(payload, "type"))
	require.Equal(t, "", WSPayloadString(payload, "model"))
	require.Equal(t, "cache-key", WSPayloadString(payload, "prompt_cache_key"))
	require.Equal(t, "resp_1", WSPayloadString(payload, "previous_response_id"))
}

func TestPopulateOpenAIUsageFromResponseJSONAcceptsChatUsageShape(t *testing.T) {
	usage := &ForwardUsage{}

	// Non-streaming WebSocket results may use Chat Completions usage fields for billing.
	PopulateUsageFromResponseJSON(
		[]byte(`{"id":"resp_1","usage":{"prompt_tokens":23,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":5}}}`),
		usage,
	)
	require.Equal(t, 23, usage.InputTokens)
	require.Equal(t, 6, usage.OutputTokens)
	require.Equal(t, 5, usage.CacheReadInputTokens)
}

func TestReplaceOpenAIWSMessageModel_OptimizedStillCorrect(t *testing.T) {
	noModel := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	require.Equal(t, string(noModel), string(ReplaceWSMessageModel(noModel, "gpt-5.1", "custom-model")))

	rootOnly := []byte(`{"type":"response.created","model":"gpt-5.1"}`)
	require.Equal(t, `{"type":"response.created","model":"custom-model"}`, string(ReplaceWSMessageModel(rootOnly, "gpt-5.1", "custom-model")))

	responseOnly := []byte(`{"type":"response.completed","response":{"model":"gpt-5.1"}}`)
	require.Equal(t, `{"type":"response.completed","response":{"model":"custom-model"}}`, string(ReplaceWSMessageModel(responseOnly, "gpt-5.1", "custom-model")))

	both := []byte(`{"model":"gpt-5.1","response":{"model":"gpt-5.1"}}`)
	require.Equal(t, `{"model":"custom-model","response":{"model":"custom-model"}}`, string(ReplaceWSMessageModel(both, "gpt-5.1", "custom-model")))
}

// TestIsOpenAIWSTokenEvent_TerminalEventsExcluded checks that terminal events do not start token timing.
// Counting response.completed or response.done as a token event when deltas are absent
// would record completion time as first-token latency (issue #2651).
func TestIsOpenAIWSTokenEvent_TerminalEventsExcluded(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		want      bool
	}{
		{name: "empty", eventType: "", want: false},
		{name: "whitespace_trimmed_empty", eventType: "   ", want: false},

		{name: "response.created", eventType: "response.created", want: false},
		{name: "response.in_progress", eventType: "response.in_progress", want: false},
		{name: "response.output_item.added", eventType: "response.output_item.added", want: false},
		{name: "response.output_item.done", eventType: "response.output_item.done", want: false},

		{name: "terminal_response.completed", eventType: "response.completed", want: false},
		{name: "terminal_response.done", eventType: "response.done", want: false},
		{name: "terminal_response.completed_padded", eventType: "  response.completed  ", want: false},
		{name: "terminal_response.done_padded", eventType: "  response.done  ", want: false},

		{name: "delta_text", eventType: "response.output_text.delta", want: true},
		{name: "delta_audio_transcript", eventType: "response.audio_transcript.delta", want: true},
		{name: "delta_function_call_arguments", eventType: "response.function_call_arguments.delta", want: true},

		{name: "output_text_done", eventType: "response.output_text.done", want: true},
		{name: "output_text_annotation_added", eventType: "response.output_text.annotation.added", want: true},

		{name: "output_audio_done", eventType: "response.output_audio.done", want: true},

		{name: "reasoning_summary_delta", eventType: "response.reasoning_summary_text.delta", want: true},

		{name: "unrelated_event_error", eventType: "error", want: false},
		{name: "unknown_event_without_match", eventType: "response.reasoning_summary_part.added", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsWSTokenEvent(tc.eventType)
			require.Equal(t, tc.want, got, "isOpenAIWSTokenEvent(%q)", tc.eventType)
		})
	}
}

// TestIsOpenAIWSTokenEvent_DisjointWithTerminal checks that terminal events are excluded from token events.
// firstTokenMs depends on this distinction to avoid the latency error in issue #2651.
func TestIsOpenAIWSTokenEvent_DisjointWithTerminal(t *testing.T) {
	terminalEvents := []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled",
	}
	for _, ev := range terminalEvents {
		t.Run(ev, func(t *testing.T) {
			require.True(t, IsWSTerminalEvent(ev), "expected terminal event %q to be classified as terminal", ev)
			require.False(t, IsWSTokenEvent(ev), "terminal event %q must NOT be classified as token event (issue #2651)", ev)
		})
	}
}

func BenchmarkReplaceOpenAIWSMessageModel_NoMatchFastPath(b *testing.B) {
	event := []byte(`{"type":"response.output_text.delta","delta":"hello world"}`)
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		benchmarkOpenAIWSBytesSink = ReplaceWSMessageModel(event, "gpt-5.1", "custom-model")
	}
}

func BenchmarkReplaceOpenAIWSMessageModel_DualReplace(b *testing.B) {
	event := []byte(`{"type":"response.completed","model":"gpt-5.1","response":{"id":"resp_1","model":"gpt-5.1"}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		benchmarkOpenAIWSBytesSink = ReplaceWSMessageModel(event, "gpt-5.1", "custom-model")
	}
}
