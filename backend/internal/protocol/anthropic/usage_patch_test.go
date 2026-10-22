package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// TestExtractSSEUsagePatchSharedFieldRules supplies the same usage fields on
// start and delta events. Speed text and explicit zero TTL buckets remain
// authoritative while invalid aggregate values follow each event's presence rules.
func TestExtractSSEUsagePatchSharedFieldRules(t *testing.T) {
	for _, eventType := range []string{"message_start", "message_delta"} {
		t.Run(eventType, func(t *testing.T) {
			usage := map[string]any{
				"speed":                       " fast ",
				"input_tokens":                "invalid",
				"output_tokens":               float64(9),
				"cache_creation_input_tokens": float64(0),
				"cache_read_input_tokens":     true,
				"cache_creation": map[string]any{
					"ephemeral_5m_input_tokens": float64(0),
					"ephemeral_1h_input_tokens": "invalid",
				},
			}
			event := map[string]any{"type": eventType, "usage": usage}
			if eventType == "message_start" {
				event = map[string]any{"type": eventType, "message": map[string]any{"usage": usage}}
			}
			patch := ExtractSSEUsagePatch(event)
			require.NotNil(t, patch)
			require.Equal(t, " fast ", patch.Speed)
			require.True(t, patch.HasSpeed)
			require.True(t, patch.HasCacheCreation5m)
			require.Zero(t, patch.CacheCreation5mTokens)
			require.False(t, patch.HasCacheCreation1h)
			require.Zero(t, patch.InputTokens)
			require.Zero(t, patch.CacheCreationInputTokens)
			require.Zero(t, patch.CacheReadInputTokens)
			if eventType == "message_start" {
				require.True(t, patch.HasInputTokens)
				require.True(t, patch.HasCacheCreationInput)
				require.True(t, patch.HasCacheReadInput)
				require.False(t, patch.HasOutputTokens)
				require.Zero(t, patch.OutputTokens)
			} else {
				require.False(t, patch.HasInputTokens)
				require.False(t, patch.HasCacheCreationInput)
				require.False(t, patch.HasCacheReadInput)
				require.True(t, patch.HasOutputTokens)
				require.Equal(t, 9, patch.OutputTokens)
			}
		})
	}
}

// TestParseSSEUsage_MessageStart supplies prompt and cache counters in an initial
// Messages event. Parsing must establish those buckets while leaving output at
// zero because message_start does not supply the generated-token observation.
func TestParseSSEUsage_MessageStart(t *testing.T) {
	usage := &protocol.TokenUsage{}

	data := `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":50,"cache_read_input_tokens":200}}}`
	ParseSSEUsage(data, usage)

	require.Equal(t, 100, usage.InputTokens)
	require.Equal(t, 50, usage.CacheCreationInputTokens)
	require.Equal(t, 200, usage.CacheReadInputTokens)
	require.Equal(t, 0, usage.OutputTokens, "message_start 不应设置 output_tokens")
}

// TestParseSSEUsage_MessageDelta supplies an output-only usage delta. Parsing
// must populate generated-token usage while leaving the independently tracked
// input counter at its existing zero value.
func TestParseSSEUsage_MessageDelta(t *testing.T) {
	usage := &protocol.TokenUsage{}

	data := `{"type":"message_delta","usage":{"output_tokens":42}}`
	ParseSSEUsage(data, usage)

	require.Equal(t, 42, usage.OutputTokens)
	require.Equal(t, 0, usage.InputTokens, "message_delta 的 output_tokens 不应影响已有的 input_tokens")
}

// TestParseSSEUsage_DeltaDoesNotOverwriteStartValues applies an output-only delta
// after an initial input observation. The input total must survive the missing
// delta field while the output total advances.
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

// TestParseSSEUsage_DeltaOverwritesWithNonZero models a compatible provider
// reporting every aggregate counter in a delta. Positive input, output, cache
// write, and cache read observations must all replace the destination values.
func TestParseSSEUsage_DeltaOverwritesWithNonZero(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// GLM-style deltas may carry all usage counters.
	ParseSSEUsage(`{"type":"message_delta","usage":{"input_tokens":200,"output_tokens":100,"cache_creation_input_tokens":30,"cache_read_input_tokens":60}}`, usage)
	require.Equal(t, 200, usage.InputTokens)
	require.Equal(t, 100, usage.OutputTokens)
	require.Equal(t, 30, usage.CacheCreationInputTokens)
	require.Equal(t, 60, usage.CacheReadInputTokens)
}

// TestParseSSEUsage_DeltaAuthoritativelyUpdatesCacheCreationBreakdown moves a
// cache-write total from the one-hour bucket to the five-minute bucket. The later
// explicit zero must clear the old category while retaining the aggregate total.
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

// TestParseSSEUsage_InvalidJSON passes an undecodable payload to the usage
// parser. The call must return with the initially empty input and output counters
// unchanged instead of exposing a decoding failure.
func TestParseSSEUsage_InvalidJSON(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// Malformed JSON must not panic.
	ParseSSEUsage("not json", usage)
	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 0, usage.OutputTokens)
}

// TestParseSSEUsage_UnknownType supplies a content event containing text. Usage
// parsing must ignore that unrelated event type and leave the destination input
// and output counters unchanged.
func TestParseSSEUsage_UnknownType(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// Unrelated event types leave usage unchanged.
	ParseSSEUsage(`{"type":"content_block_delta","delta":{"text":"hello"}}`, usage)
	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 0, usage.OutputTokens)
}

// TestParseSSEUsage_EmptyString models a stream payload with no event data.
// Parsing must tolerate the empty input and retain the destination input counter
// at its initial zero value.
func TestParseSSEUsage_EmptyString(t *testing.T) {
	usage := &protocol.TokenUsage{}

	ParseSSEUsage("", usage)
	require.Equal(t, 0, usage.InputTokens)
}

// TestParseSSEUsage_DoneEvent supplies the non-JSON completion sentinel to the
// usage parser. The sentinel must leave the asserted input counter at zero
// because it contains no Messages usage observation.
func TestParseSSEUsage_DoneEvent(t *testing.T) {
	usage := &protocol.TokenUsage{}

	// [DONE] leaves the accumulated usage unchanged.
	ParseSSEUsage("[DONE]", usage)
	require.Equal(t, 0, usage.InputTokens)
}

// BenchmarkGatewayService_ParseSSEUsage_MessageStart measures decoding an
// initial event with aggregate prompt and TTL cache counters. Each iteration uses
// a fresh usage destination so allocation measurements include patch extraction
// and its application.
func BenchmarkGatewayService_ParseSSEUsage_MessageStart(b *testing.B) {
	data := `{"type":"message_start","message":{"usage":{"input_tokens":123,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage := &protocol.TokenUsage{}
		ParseSSEUsage(data, usage)
	}
}

// BenchmarkGatewayService_ParseSSEUsage_MessageDelta measures decoding a delta
// with output and both TTL cache buckets. Each iteration uses a fresh destination
// to measure the complete parse and merge operation.
func BenchmarkGatewayService_ParseSSEUsage_MessageDelta(b *testing.B) {
	data := `{"type":"message_delta","usage":{"output_tokens":456,"cache_creation_input_tokens":30,"cache_read_input_tokens":7,"cached_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage := &protocol.TokenUsage{}
		ParseSSEUsage(data, usage)
	}
}
