package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var (
	benchmarkWSParseStringSink string
	benchmarkWSParseMapSink    map[string]any

	// Benchmark sinks retain the response ID and response-object presence.
	benchmarkOpenAIWSStringSink string
	benchmarkOpenAIWSBoolSink   bool
)

func TestParseOpenAIWSEventEnvelope(t *testing.T) {
	eventType, responseID, response := ParseWSEventEnvelope([]byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.1"}}`))
	require.Equal(t, "response.completed", eventType)
	require.Equal(t, "resp_1", responseID)
	require.True(t, response.Exists())
	require.Equal(t, `{"id":"resp_1","model":"gpt-5.1"}`, response.Raw)

	eventType, responseID, response = ParseWSEventEnvelope([]byte(`{"type":"response.delta","id":"evt_1"}`))
	require.Equal(t, "response.delta", eventType)
	require.Equal(t, "evt_1", responseID)
	require.False(t, response.Exists())
}

func TestParseOpenAIWSResponseUsageFromCompletedEvent(t *testing.T) {
	usage := &ForwardUsage{}
	ParseWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`),
		usage,
	)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadInputTokens)
	ParseWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"prompt_tokens":19,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}}`),
		usage,
	)
	require.Equal(t, 19, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
	ParseWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`),
		usage,
	)
	require.Equal(t, ForwardUsage{InputTokens: 19, OutputTokens: 5, CacheReadInputTokens: 4}, *usage)
	ParseWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.failed","response":{"usage":{"input_tokens":3,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`),
		usage,
	)
	require.Equal(t, ForwardUsage{InputTokens: 3}, *usage)
}

func TestOpenAIWSEventShouldParseUsageTerminalEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled",
	} {
		require.True(t, WSEventShouldParseUsage(eventType), eventType)
		require.True(t, WSEventShouldParseUsage("  "+eventType+"  "), eventType)
	}
	require.False(t, WSEventShouldParseUsage("response.output_text.delta"))
	require.True(t, WSEventShouldParseUsage("response.output_text.done"))
	require.False(t, WSEventShouldParseUsage(""))
	require.False(t, WSMessageShouldParseUsage("response.in_progress", []byte(`{"type":"response.in_progress"}`)))
	require.True(t, WSMessageShouldParseUsage("response.in_progress", []byte(`{"type":"response.in_progress","usage":{}}`)))
	require.False(t, WSMessageShouldParseUsage("response.output_text.delta", []byte(`{"type":"response.output_text.delta","usage":{}}`)))
}

func TestOpenAIWSMessageLikelyContainsToolCalls(t *testing.T) {
	require.False(t, WSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_text.delta","delta":"hello"}`)))
	require.True(t, WSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_item.added","item":{"tool_calls":[{"id":"tc1"}]}}`)))
	require.True(t, WSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_item.added","item":{"type":"function_call"}}`)))
}

func BenchmarkWSIngressPayloadParseLegacy(b *testing.B) {
	raw := benchmarkWSIngressPayloadBytes()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		eventType, model, promptCacheKey, previousResponseID, payload, err := legacyParseWSIngressPayload(raw)
		if err == nil {
			benchmarkWSParseStringSink = eventType + model + promptCacheKey + previousResponseID
			benchmarkWSParseMapSink = payload
		}
	}
}

func BenchmarkWSIngressPayloadParseOptimized(b *testing.B) {
	raw := benchmarkWSIngressPayloadBytes()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		eventType, model, promptCacheKey, previousResponseID, payload, err := optimizedParseWSIngressPayload(raw)
		if err == nil {
			benchmarkWSParseStringSink = eventType + model + promptCacheKey + previousResponseID
			benchmarkWSParseMapSink = payload
		}
	}
}

func benchmarkWSIngressPayloadBytes() []byte {
	return []byte(`{"type":"response.create","model":"gpt-5.3-codex","prompt_cache_key":"cache_bench","previous_response_id":"resp_prev_bench","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
}

func legacyParseWSIngressPayload(raw []byte) (eventType, model, promptCacheKey, previousResponseID string, payload map[string]any, err error) {
	values := gjson.GetManyBytes(raw, "type", "model", "prompt_cache_key", "previous_response_id")
	eventType = strings.TrimSpace(values[0].String())
	if eventType == "" {
		eventType = "response.create"
	}
	model = strings.TrimSpace(values[1].String())
	promptCacheKey = strings.TrimSpace(values[2].String())
	previousResponseID = strings.TrimSpace(values[3].String())
	payload = make(map[string]any)
	if err = json.Unmarshal(raw, &payload); err != nil {
		return "", "", "", "", nil, err
	}
	if _, exists := payload["type"]; !exists {
		payload["type"] = "response.create"
	}
	return eventType, model, promptCacheKey, previousResponseID, payload, nil
}

func optimizedParseWSIngressPayload(raw []byte) (eventType, model, promptCacheKey, previousResponseID string, payload map[string]any, err error) {
	payload = make(map[string]any)
	if err = json.Unmarshal(raw, &payload); err != nil {
		return "", "", "", "", nil, err
	}
	eventType = WSPayloadString(payload, "type")
	if eventType == "" {
		eventType = "response.create"
		payload["type"] = eventType
	}
	model = WSPayloadString(payload, "model")
	promptCacheKey = WSPayloadString(payload, "prompt_cache_key")
	previousResponseID = WSPayloadString(payload, "previous_response_id")
	return eventType, model, promptCacheKey, previousResponseID, payload, nil
}

func BenchmarkOpenAIWSEventEnvelopeParse(b *testing.B) {
	event := []byte(`{"type":"response.completed","response":{"id":"resp_bench_1","model":"gpt-5.1","usage":{"input_tokens":12,"output_tokens":8}}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, responseID, response := ParseWSEventEnvelope(event)
		benchmarkOpenAIWSStringSink = responseID
		benchmarkOpenAIWSBoolSink = response.Exists()
	}
}
