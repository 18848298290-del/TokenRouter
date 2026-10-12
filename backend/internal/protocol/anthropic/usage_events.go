package anthropic

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// ParseSSEUsagePassthrough extracts usage from Anthropic SSE data shared by
// Anthropic passthrough and Chinese providers' Anthropic-compatible streams.
func ParseSSEUsagePassthrough(data string, usage *protocol.TokenUsage) {
	if usage == nil || data == "" || data == "[DONE]" {
		return
	}

	parsed := gjson.Parse(data)
	switch parsed.Get("type").String() {
	case "message_start":
		msgUsage := parsed.Get("message.usage")
		if msgUsage.Exists() {
			if speed := strings.TrimSpace(msgUsage.Get("speed").String()); speed != "" {
				usage.Speed = speed
			}
			usage.InputTokens = int(msgUsage.Get("input_tokens").Int())
			usage.CacheCreationInputTokens = int(msgUsage.Get("cache_creation_input_tokens").Int())
			usage.CacheReadInputTokens = int(msgUsage.Get("cache_read_input_tokens").Int())

			// message_start can replace 5m/1h details with zero, as in the general parser.
			cc5m := msgUsage.Get("cache_creation.ephemeral_5m_input_tokens")
			cc1h := msgUsage.Get("cache_creation.ephemeral_1h_input_tokens")
			if cc5m.Exists() || cc1h.Exists() {
				usage.CacheCreation5mTokens = int(cc5m.Int())
				usage.CacheCreation1hTokens = int(cc1h.Int())
			}
		}
	case "message_delta":
		deltaUsage := parsed.Get("usage")
		if deltaUsage.Exists() {
			if speed := strings.TrimSpace(deltaUsage.Get("speed").String()); speed != "" {
				usage.Speed = speed
			}
			if v := deltaUsage.Get("input_tokens").Int(); v > 0 {
				usage.InputTokens = int(v)
			}
			if v := deltaUsage.Get("output_tokens").Int(); v > 0 {
				usage.OutputTokens = int(v)
			}
			if v := deltaUsage.Get("cache_creation_input_tokens").Int(); v > 0 {
				usage.CacheCreationInputTokens = int(v)
			}
			if v := deltaUsage.Get("cache_read_input_tokens").Int(); v > 0 {
				usage.CacheReadInputTokens = int(v)
			}

			cc5m := deltaUsage.Get("cache_creation.ephemeral_5m_input_tokens")
			cc1h := deltaUsage.Get("cache_creation.ephemeral_1h_input_tokens")
			if cc5m.Exists() {
				usage.CacheCreation5mTokens = int(cc5m.Int())
			}
			if cc1h.Exists() {
				usage.CacheCreation1hTokens = int(cc1h.Int())
			}
		}
	}

	if usage.CacheReadInputTokens == 0 {
		if cached := parsed.Get("message.usage.cached_tokens").Int(); cached > 0 {
			usage.CacheReadInputTokens = int(cached)
		}
		if cached := parsed.Get("usage.cached_tokens").Int(); usage.CacheReadInputTokens == 0 && cached > 0 {
			usage.CacheReadInputTokens = int(cached)
		}
	}
	if usage.CacheCreationInputTokens == 0 {
		cc5m := parsed.Get("message.usage.cache_creation.ephemeral_5m_input_tokens").Int()
		cc1h := parsed.Get("message.usage.cache_creation.ephemeral_1h_input_tokens").Int()
		if cc5m == 0 && cc1h == 0 {
			cc5m = parsed.Get("usage.cache_creation.ephemeral_5m_input_tokens").Int()
			cc1h = parsed.Get("usage.cache_creation.ephemeral_1h_input_tokens").Int()
		}
		total := cc5m + cc1h
		if total > 0 {
			usage.CacheCreationInputTokens = int(total)
		}
	}

	// Kimi uses input_tokens for total input at message_start and uncached input at message_delta.
	// prompt_tokens is the total in both events; normalize it into separate billing buckets.
	usageNode := parsed.Get("usage")
	if parsed.Get("type").String() == "message_start" {
		usageNode = parsed.Get("message.usage")
	}
	NormalizeAnthropicCompatiblePromptUsage(usageNode, usage)
}

// NormalizeAnthropicCompatiblePromptUsage converts OpenAI-style prompt/cache
// aliases into mutually exclusive Claude usage buckets. It returns whether
// normalization was applied; responses without these aliases remain unchanged.
func NormalizeAnthropicCompatiblePromptUsage(usageNode gjson.Result, usage *protocol.TokenUsage) bool {
	if usage == nil || !usageNode.Exists() {
		return false
	}
	promptTokens := usageNode.Get("prompt_tokens")
	promptCacheHitTokens := usageNode.Get("prompt_cache_hit_tokens")
	promptCacheMissTokens := usageNode.Get("prompt_cache_miss_tokens")
	if (!promptTokens.Exists() || promptTokens.Int() <= 0) &&
		!promptCacheHitTokens.Exists() && !promptCacheMissTokens.Exists() {
		return false
	}

	cacheReadTokens := usage.CacheReadInputTokens
	if v := usageNode.Get("cache_read_input_tokens"); v.Exists() {
		cacheReadTokens = int(v.Int())
	}
	if cacheReadTokens == 0 {
		if v := usageNode.Get("cached_tokens"); v.Exists() {
			cacheReadTokens = int(v.Int())
		}
	}
	if cacheReadTokens == 0 {
		if v := usageNode.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
			cacheReadTokens = int(v.Int())
		}
	}
	if cacheReadTokens == 0 && promptCacheHitTokens.Exists() {
		cacheReadTokens = max(int(promptCacheHitTokens.Int()), 0)
	}

	cacheCreationTokens := usage.CacheCreationInputTokens
	if v := usageNode.Get("cache_creation_input_tokens"); v.Exists() {
		cacheCreationTokens = int(v.Int())
	}
	if cacheCreationTokens == 0 {
		cc5m := usageNode.Get("cache_creation.ephemeral_5m_input_tokens").Int()
		cc1h := usageNode.Get("cache_creation.ephemeral_1h_input_tokens").Int()
		if cc5m > 0 || cc1h > 0 {
			cacheCreationTokens = int(cc5m + cc1h)
		}
	}

	if promptCacheMissTokens.Exists() {
		usage.InputTokens = max(int(promptCacheMissTokens.Int()), 0)
	} else {
		usage.InputTokens = max(int(promptTokens.Int())-cacheReadTokens-cacheCreationTokens, 0)
	}
	usage.CacheReadInputTokens = cacheReadTokens
	usage.CacheCreationInputTokens = cacheCreationTokens
	return true
}

func ParseClaudeUsageFromResponseBody(body []byte) *protocol.TokenUsage {
	usage := &protocol.TokenUsage{}
	if len(body) == 0 {
		return usage
	}

	parsed := gjson.ParseBytes(body)
	usageNode := parsed.Get("usage")
	if !usageNode.Exists() {
		return usage
	}

	usage.InputTokens = int(usageNode.Get("input_tokens").Int())
	usage.OutputTokens = int(usageNode.Get("output_tokens").Int())
	usage.CacheCreationInputTokens = int(usageNode.Get("cache_creation_input_tokens").Int())
	usage.CacheReadInputTokens = int(usageNode.Get("cache_read_input_tokens").Int())
	usage.Speed = strings.TrimSpace(usageNode.Get("speed").String())

	cc5m := usageNode.Get("cache_creation.ephemeral_5m_input_tokens").Int()
	cc1h := usageNode.Get("cache_creation.ephemeral_1h_input_tokens").Int()
	if cc5m > 0 || cc1h > 0 {
		usage.CacheCreation5mTokens = int(cc5m)
		usage.CacheCreation1hTokens = int(cc1h)
	}
	if usage.CacheCreationInputTokens == 0 && (cc5m > 0 || cc1h > 0) {
		usage.CacheCreationInputTokens = int(cc5m + cc1h)
	}
	if usage.CacheReadInputTokens == 0 {
		if cached := usageNode.Get("cached_tokens").Int(); cached > 0 {
			usage.CacheReadInputTokens = int(cached)
		}
	}
	NormalizeAnthropicCompatiblePromptUsage(usageNode, usage)
	return usage
}
