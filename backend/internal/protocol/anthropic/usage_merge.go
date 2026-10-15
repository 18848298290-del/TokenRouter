package anthropic

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// usage_merge.go merges Anthropic usage observations into disjoint prompt and
// cache accounting buckets.
// Anthropic-compatible responses can describe prompt work as uncached tokens,
// a total prompt count, or separate cache hit and miss buckets. Converted
// Messages streams need disjoint input and cache buckets for billing, even
// when later events revise how the same prompt work is represented.
//
// MergeAnthropicUsage uses total or hit/miss evidence when supplied, subtracts
// cached work from a total, and clamps calculated uncached counts to zero.
// The ordinary Anthropic path updates positive counters and retains previous
// observations for omitted or zero counters. Positive canonical cache fields
// take precedence over compatible aliases, and nonblank speed observations
// survive alongside the token counts for completion-time pricing.

// MergeAnthropicUsage merges a Messages usage observation into billing buckets.
// A nil destination is ignored. Prompt totals or hit/miss pointers select the
// compatible-provider calculation, where a supplied miss bucket is
// authoritative even when it is zero.
//
// Otherwise, positive Anthropic counters update the destination while omitted
// or zero counters retain prior observations. Canonical cache counters override
// aliases when positive, and a nonblank speed string is copied as supplied.
// Callers serialize updates to the shared destination across stream events.
func MergeAnthropicUsage(dst *protocol.TokenUsage, src AnthropicUsage) {
	if dst == nil {
		return
	}

	// Compatible prompt totals include cached work; split those totals before
	// billing so cache hits and cache writes are not charged as uncached input.
	if src.PromptTokens > 0 || src.PromptCacheHitTokens != nil || src.PromptCacheMissTokens != nil {
		cacheReadTokens := src.CacheReadInputTokens
		// Canonical cache reads take precedence. Resolve missing reads through
		// compatible aliases in order so one observation contributes one cache bucket.
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
			// A supplied miss count already excludes cached work, including the
			// fully cached zero case; subtracting cache reads again would erase valid input.
			dst.InputTokens = max(*src.PromptCacheMissTokens, 0)
		} else {
			// Cache read and creation tokens belong to separate billing buckets.
			// Clamp inconsistent totals so normalization cannot create negative uncached usage.
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
			// Some compatible Messages responses expose cache reads as cached_tokens.
			// Use that alias when the canonical count contributes no positive observation.
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
