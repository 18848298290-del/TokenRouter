package anthropic

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

func MergeAnthropicUsage(dst *protocol.TokenUsage, src AnthropicUsage) {
	if dst == nil {
		return
	}

	// Anthropic-compatible providers may also return OpenAI prompt/cache fields.
	// Prefer totals or hit/miss buckets because Kimi's input_tokens meaning varies
	// by event; also accept GLM and DeepSeek cache aliases.
	if src.PromptTokens > 0 || src.PromptCacheHitTokens != nil || src.PromptCacheMissTokens != nil {
		cacheReadTokens := src.CacheReadInputTokens
		if cacheReadTokens == 0 && src.CachedTokens > 0 {
			cacheReadTokens = src.CachedTokens
		}
		if cacheReadTokens == 0 && src.PromptTokensDetails != nil && src.PromptTokensDetails.CachedTokens > 0 {
			cacheReadTokens = src.PromptTokensDetails.CachedTokens
		}
		if cacheReadTokens == 0 && src.PromptCacheHitTokens != nil {
			cacheReadTokens = max(*src.PromptCacheHitTokens, 0)
		}

		if src.PromptCacheMissTokens != nil {
			dst.InputTokens = max(*src.PromptCacheMissTokens, 0)
		} else {
			dst.InputTokens = max(src.PromptTokens-cacheReadTokens-src.CacheCreationInputTokens, 0)
		}
		dst.CacheReadInputTokens = cacheReadTokens
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	} else {
		if src.InputTokens > 0 {
			dst.InputTokens = src.InputTokens
		}
		if src.CacheReadInputTokens > 0 {
			dst.CacheReadInputTokens = src.CacheReadInputTokens
		} else if src.CachedTokens > 0 {
			dst.CacheReadInputTokens = src.CachedTokens
		}
		if src.CacheCreationInputTokens > 0 {
			dst.CacheCreationInputTokens = src.CacheCreationInputTokens
		}
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if strings.TrimSpace(src.Speed) != "" {
		dst.Speed = src.Speed
	}
}
