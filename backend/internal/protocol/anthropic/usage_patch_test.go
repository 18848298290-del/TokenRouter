package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

func TestParseSSEUsage_MessageStart(t *testing.T) {
	usage := &protocol.TokenUsage{}

	data := `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":50,"cache_read_input_tokens":200}}}`
	ParseSSEUsage(data, usage)

	require.Equal(t, 100, usage.InputTokens)
	require.Equal(t, 50, usage.CacheCreationInputTokens)
	require.Equal(t, 200, usage.CacheReadInputTokens)
	require.Equal(t, 0, usage.OutputTokens, "message_start 不应设置 output_tokens")
}

func TestParseSSEUsage_MessageDelta(t *testing.T) {
	usage := &protocol.TokenUsage{}

	data := `{"type":"message_delta","usage":{"output_tokens":42}}`
	ParseSSEUsage(data, usage)

	require.Equal(t, 42, usage.OutputTokens)
	require.Equal(t, 0, usage.InputTokens, "message_delta 的 output_tokens 不应影响已有的 input_tokens")
}

func TestParseSSEUsage_DeltaDoesNotOverwriteStartValues(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// Seed the counters from message_start.
	ParseSSEUsage(`{"type":"message_start","message":{"usage":{"input_tokens":100}}}`, usage)
	require.Equal(t, 100, usage.InputTokens)

	// The delta supplies output_tokens and omits input_tokens.
	ParseSSEUsage(`{"type":"message_delta","usage":{"output_tokens":50}}`, usage)
	require.Equal(t, 100, usage.InputTokens, "delta 中 input_tokens=0 不应覆盖 start 中的值")
	require.Equal(t, 50, usage.OutputTokens)
}

func TestParseSSEUsage_DeltaOverwritesWithNonZero(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// GLM-style deltas may carry all usage counters.
	ParseSSEUsage(`{"type":"message_delta","usage":{"input_tokens":200,"output_tokens":100,"cache_creation_input_tokens":30,"cache_read_input_tokens":60}}`, usage)
	require.Equal(t, 200, usage.InputTokens)
	require.Equal(t, 100, usage.OutputTokens)
	require.Equal(t, 30, usage.CacheCreationInputTokens)
	require.Equal(t, 60, usage.CacheReadInputTokens)
}

func TestParseSSEUsage_DeltaAuthoritativelyUpdatesCacheCreationBreakdown(t *testing.T) {
	usage := &protocol.TokenUsage{}

	ParseSSEUsage(`{"type":"message_start","message":{"usage":{"cache_creation_input_tokens":463184,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":463184}}}}`, usage)
	require.Equal(t, 463184, usage.CacheCreationInputTokens)
	require.Equal(t, 0, usage.CacheCreation5mTokens)
	require.Equal(t, 463184, usage.CacheCreation1hTokens)

	ParseSSEUsage(`{"type":"message_delta","usage":{"cache_creation_input_tokens":463184,"cache_creation":{"ephemeral_5m_input_tokens":463184,"ephemeral_1h_input_tokens":0}}}`, usage)
	require.Equal(t, 463184, usage.CacheCreationInputTokens)
	require.Equal(t, 463184, usage.CacheCreation5mTokens)
	require.Equal(t, 0, usage.CacheCreation1hTokens)
}

func TestParseSSEUsage_InvalidJSON(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// Malformed JSON must not panic.
	ParseSSEUsage("not json", usage)
	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 0, usage.OutputTokens)
}

func TestParseSSEUsage_UnknownType(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// Unrelated event types leave usage unchanged.
	ParseSSEUsage(`{"type":"content_block_delta","delta":{"text":"hello"}}`, usage)
	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 0, usage.OutputTokens)
}

func TestParseSSEUsage_EmptyString(t *testing.T) {
	usage := &protocol.TokenUsage{}

	ParseSSEUsage("", usage)
	require.Equal(t, 0, usage.InputTokens)
}

func TestParseSSEUsage_DoneEvent(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// [DONE] leaves the accumulated usage unchanged.
	ParseSSEUsage("[DONE]", usage)
	require.Equal(t, 0, usage.InputTokens)
}

func BenchmarkGatewayService_ParseSSEUsage_MessageStart(b *testing.B) {
	data := `{"type":"message_start","message":{"usage":{"input_tokens":123,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage := &protocol.TokenUsage{}
		ParseSSEUsage(data, usage)
	}
}

func BenchmarkGatewayService_ParseSSEUsage_MessageDelta(b *testing.B) {
	data := `{"type":"message_delta","usage":{"output_tokens":456,"cache_creation_input_tokens":30,"cache_read_input_tokens":7,"cached_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage := &protocol.TokenUsage{}
		ParseSSEUsage(data, usage)
	}
}
