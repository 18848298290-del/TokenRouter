package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

func TestParseSSEUsagePassthroughNormalizesKimiPromptUsage(t *testing.T) {
	usage := &protocol.TokenUsage{}

	ParseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":173306,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"prompt_tokens":173306,"cached_tokens":0}}}`, usage)
	require.Equal(t, 173306, usage.InputTokens)
	require.Zero(t, usage.CacheReadInputTokens)

	ParseSSEUsagePassthrough(`{"type":"message_delta","usage":{"input_tokens":250,"cache_creation_input_tokens":0,"cache_read_input_tokens":173056,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`, usage)
	require.Equal(t, 250, usage.InputTokens, "Kimi message_delta input_tokens is already the uncached bucket")
	require.Equal(t, 173056, usage.CacheReadInputTokens)
	require.Equal(t, 166, usage.OutputTokens)
}

func TestParseSSEUsagePassthroughKimiFullyCachedInputReplacesStartTotal(t *testing.T) {
	usage := &protocol.TokenUsage{}

	ParseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":173306,"prompt_tokens":173306}}}`, usage)
	ParseSSEUsagePassthrough(`{"type":"message_delta","usage":{"input_tokens":0,"cache_read_input_tokens":173306,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306}}`, usage)

	require.Zero(t, usage.InputTokens, "an explicit zero uncached bucket must not retain message_start's total")
	require.Equal(t, 173306, usage.CacheReadInputTokens)
}

func TestParseClaudeUsageFromResponseBodyNormalizesCNProviderAliases(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantInput     int
		wantCacheRead int
		wantOutput    int
	}{
		{
			name:          "Kimi top-level cached_tokens",
			body:          `{"usage":{"input_tokens":173306,"output_tokens":166,"cache_read_input_tokens":173056,"prompt_tokens":173306,"cached_tokens":173056}}`,
			wantInput:     250,
			wantCacheRead: 173056,
			wantOutput:    166,
		},
		{
			name:          "GLM nested prompt cache details",
			body:          `{"usage":{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`,
			wantInput:     400,
			wantCacheRead: 800,
			wantOutput:    300,
		},
		{
			name:          "DeepSeek prompt cache hit and miss buckets",
			body:          `{"usage":{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}`,
			wantInput:     400,
			wantCacheRead: 800,
			wantOutput:    300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := ParseClaudeUsageFromResponseBody([]byte(tt.body))
			require.Equal(t, tt.wantInput, usage.InputTokens)
			require.Equal(t, tt.wantCacheRead, usage.CacheReadInputTokens)
			require.Equal(t, tt.wantOutput, usage.OutputTokens)
		})
	}
}

func TestParseSSEUsagePassthroughNormalizesGLMAndDeepSeekAliases(t *testing.T) {
	tests := []struct {
		name          string
		data          string
		wantInput     int
		wantCacheRead int
	}{
		{
			name:          "GLM",
			data:          `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`,
			wantInput:     400,
			wantCacheRead: 800,
		},
		{
			name:          "DeepSeek",
			data:          `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}`,
			wantInput:     400,
			wantCacheRead: 800,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := &protocol.TokenUsage{}
			ParseSSEUsagePassthrough(tt.data, usage)
			require.Equal(t, tt.wantInput, usage.InputTokens)
			require.Equal(t, tt.wantCacheRead, usage.CacheReadInputTokens)
			require.Equal(t, 30, usage.OutputTokens)
		})
	}
}

func TestGatewayService_ParseSSEUsagePassthrough_MessageStartFallbacks(t *testing.T) {
	usage := &protocol.TokenUsage{}
	data := `{"type":"message_start","message":{"usage":{"input_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cached_tokens":9,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}}}}`

	ParseSSEUsagePassthrough(data, usage)

	require.Equal(t, 12, usage.InputTokens)
	require.Equal(t, 9, usage.CacheReadInputTokens, "应兼容 cached_tokens 字段")
	require.Equal(t, 7, usage.CacheCreationInputTokens, "聚合字段为空时应从 5m/1h 明细回填")
	require.Equal(t, 3, usage.CacheCreation5mTokens)
	require.Equal(t, 4, usage.CacheCreation1hTokens)
}

func TestGatewayService_ParseSSEUsagePassthrough_MessageDeltaSelectiveOverwrite(t *testing.T) {
	usage := &protocol.TokenUsage{}
	start := `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":463184,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":463184}}}}`
	ParseSSEUsagePassthrough(start, usage)

	data := `{"type":"message_delta","usage":{"input_tokens":0,"output_tokens":5,"cache_creation_input_tokens":463184,"cache_read_input_tokens":0,"cached_tokens":11,"cache_creation":{"ephemeral_5m_input_tokens":463184,"ephemeral_1h_input_tokens":0}}}`

	ParseSSEUsagePassthrough(data, usage)

	require.Equal(t, 10, usage.InputTokens, "message_delta 中 0 值不应覆盖已有 input_tokens")
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 463184, usage.CacheCreationInputTokens)
	require.Equal(t, 11, usage.CacheReadInputTokens, "cache_read_input_tokens 为空时应回退到 cached_tokens")
	require.Equal(t, 463184, usage.CacheCreation5mTokens)
	require.Equal(t, 0, usage.CacheCreation1hTokens)
}

func TestGatewayService_ParseSSEUsagePassthrough_NoopCases(t *testing.T) {
	usage := &protocol.TokenUsage{InputTokens: 3}
	ParseSSEUsagePassthrough("", usage)
	require.Equal(t, 3, usage.InputTokens)

	ParseSSEUsagePassthrough("[DONE]", usage)
	require.Equal(t, 3, usage.InputTokens)

	ParseSSEUsagePassthrough("not-json", usage)
	require.Equal(t, 3, usage.InputTokens)

	// A nil destination must not panic.
	ParseSSEUsagePassthrough(`{"type":"message_start"}`, nil)
}

func TestGatewayService_ParseSSEUsagePassthrough_FallbackFromUsageNode(t *testing.T) {
	usage := &protocol.TokenUsage{}
	data := `{"type":"content_block_delta","usage":{"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":1}}}`

	ParseSSEUsagePassthrough(data, usage)

	require.Equal(t, 6, usage.CacheReadInputTokens)
	require.Equal(t, 3, usage.CacheCreationInputTokens)
}

func TestParseClaudeUsageFromResponseBody(t *testing.T) {
	t.Run("empty or missing usage", func(t *testing.T) {
		got := ParseClaudeUsageFromResponseBody(nil)
		require.NotNil(t, got)
		require.Equal(t, 0, got.InputTokens)

		got = ParseClaudeUsageFromResponseBody([]byte(`{"id":"x"}`))
		require.NotNil(t, got)
		require.Equal(t, 0, got.OutputTokens)
	})

	t.Run("parse all usage fields and fallback", func(t *testing.T) {
		body := []byte(`{"usage":{"input_tokens":21,"output_tokens":34,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cached_tokens":13,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":8}}}`)
		got := ParseClaudeUsageFromResponseBody(body)
		require.Equal(t, 21, got.InputTokens)
		require.Equal(t, 34, got.OutputTokens)
		require.Equal(t, 13, got.CacheReadInputTokens, "cache_read_input_tokens 为空时应回退 cached_tokens")
		require.Equal(t, 13, got.CacheCreationInputTokens, "聚合字段为空时应由 5m/1h 回填")
		require.Equal(t, 5, got.CacheCreation5mTokens)
		require.Equal(t, 8, got.CacheCreation1hTokens)
	})

	t.Run("keep explicit aggregate values", func(t *testing.T) {
		body := []byte(`{"usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":9,"cache_read_input_tokens":7,"cached_tokens":99,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":5}}}`)
		got := ParseClaudeUsageFromResponseBody(body)
		require.Equal(t, 9, got.CacheCreationInputTokens, "已显式提供聚合字段时不应被明细覆盖")
		require.Equal(t, 7, got.CacheReadInputTokens, "已显式提供 cache_read_input_tokens 时不应回退 cached_tokens")
	})
}

func BenchmarkGatewayService_ParseSSEUsagePassthrough_MessageStart(b *testing.B) {
	data := `{"type":"message_start","message":{"usage":{"input_tokens":123,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage := &protocol.TokenUsage{}
		ParseSSEUsagePassthrough(data, usage)
	}
}

func BenchmarkGatewayService_ParseSSEUsagePassthrough_MessageDelta(b *testing.B) {
	data := `{"type":"message_delta","usage":{"output_tokens":456,"cache_creation_input_tokens":30,"cache_read_input_tokens":7,"cached_tokens":7,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage := &protocol.TokenUsage{}
		ParseSSEUsagePassthrough(data, usage)
	}
}

func BenchmarkParseClaudeUsageFromResponseBody(b *testing.B) {
	body := []byte(`{"id":"msg_123","type":"message","usage":{"input_tokens":123,"output_tokens":456,"cache_creation_input_tokens":45,"cache_read_input_tokens":6,"cached_tokens":6,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":25}}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = ParseClaudeUsageFromResponseBody(body)
	}
}
