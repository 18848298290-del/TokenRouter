package openaiforward

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// TestMergeAnthropicUsageNormalizesKimiStreamForOpenAIBilling merges start and
// terminal usage from a Kimi stream fixture. Normalized uncached and cache-read
// buckets must reconstruct the inclusive OpenAI input total while retaining
// output tokens.
func TestMergeAnthropicUsageNormalizesKimiStreamForOpenAIBilling(t *testing.T) {
	var start protocolanthropic.AnthropicStreamEvent
	require.NoError(t, json.Unmarshal([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":173306,"prompt_tokens":173306,"cached_tokens":0}}}`), &start))
	var delta protocolanthropic.AnthropicStreamEvent
	require.NoError(t, json.Unmarshal([]byte(`{"type":"message_delta","usage":{"input_tokens":250,"cache_read_input_tokens":173056,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`), &delta))

	usage := &protocol.TokenUsage{}
	protocolanthropic.MergeAnthropicUsage(usage, start.Message.Usage)
	protocolanthropic.MergeAnthropicUsage(usage, *delta.Usage)
	require.Equal(t, 250, usage.InputTokens)
	require.Equal(t, 173056, usage.CacheReadInputTokens)

	openAIUsage := AnthropicUsageToOpenAI(usage)
	require.Equal(t, 173306, openAIUsage.InputTokens, "OpenAI gateway expects an inclusive input total")
	require.Equal(t, 250, openAIUsage.InputTokens-openAIUsage.CacheReadInputTokens-openAIUsage.CacheCreationInputTokens)
	require.Equal(t, 166, openAIUsage.OutputTokens)
}

// TestMergeAnthropicUsageNormalizesGLMAndDeepSeekAliases parses provider cache
// aliases from buffered usage fixtures. Both encodings must yield separate
// uncached and cache-read buckets whose conversion recovers the inclusive input
// count.
func TestMergeAnthropicUsageNormalizesGLMAndDeepSeekAliases(t *testing.T) {
	tests := []struct {
		name string // provider fixture name
		raw  string // encoded usage response
	}{
		{
			name: "GLM",
			raw:  `{"input_tokens":1200,"output_tokens":30,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}`,
		},
		{
			name: "DeepSeek",
			raw:  `{"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var src protocolanthropic.AnthropicUsage
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &src))

			usage := &protocol.TokenUsage{}
			protocolanthropic.MergeAnthropicUsage(usage, src)
			require.Equal(t, 400, usage.InputTokens)
			require.Equal(t, 800, usage.CacheReadInputTokens)

			openAIUsage := AnthropicUsageToOpenAI(usage)
			require.Equal(t, 1200, openAIUsage.InputTokens)
			require.Equal(t, 400, openAIUsage.InputTokens-openAIUsage.CacheReadInputTokens-openAIUsage.CacheCreationInputTokens)
		})
	}
}

// TestClaudeUsageToOpenAIUsagePreservesCNProviderNativeAnthropicBuckets
// converts already-normalized Anthropic token buckets for two CN fixtures.
// Cache creation and reads must remain separate while their sum with uncached
// input forms the OpenAI total.
func TestClaudeUsageToOpenAIUsagePreservesCNProviderNativeAnthropicBuckets(t *testing.T) {
	tests := []struct {
		name         string              // provider fixture name
		usage        protocol.TokenUsage // normalized token buckets
		wantTotal    int                 // expected inclusive input
		wantUncached int                 // expected uncached input
	}{
		{
			name: "GLM",
			usage: protocol.TokenUsage{
				InputTokens:              2,
				OutputTokens:             302,
				CacheCreationInputTokens: 733,
				CacheReadInputTokens:     376156,
			},
			wantTotal:    376891,
			wantUncached: 2,
		},
		{
			name: "DeepSeek",
			usage: protocol.TokenUsage{
				InputTokens:          400,
				OutputTokens:         30,
				CacheReadInputTokens: 800,
			},
			wantTotal:    1200,
			wantUncached: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openAIUsage := AnthropicUsageToOpenAI(&tt.usage)
			require.Equal(t, tt.wantTotal, openAIUsage.InputTokens)
			require.Equal(t, tt.wantUncached, openAIUsage.InputTokens-openAIUsage.CacheReadInputTokens-openAIUsage.CacheCreationInputTokens)
			require.Equal(t, tt.usage.CacheReadInputTokens, openAIUsage.CacheReadInputTokens)
			require.Equal(t, tt.usage.CacheCreationInputTokens, openAIUsage.CacheCreationInputTokens)
		})
	}
}
