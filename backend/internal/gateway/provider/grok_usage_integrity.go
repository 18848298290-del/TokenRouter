package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// HasBillableGrokChatUsage reports whether any aggregate billing token count is positive.
// Detail fields alone do not establish billable usage.
func HasBillableGrokChatUsage(usage openai.ForwardUsage) bool {
	return usage.InputTokens > 0 ||
		usage.OutputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 ||
		usage.CacheReadInputTokens > 0
}

// RequiresBillableGrokChatUsage recognizes Grok traffic by provider platform or model identity.
// Model matching also covers Grok requests sent through OpenAI-compatible providers.
func RequiresBillableGrokChatUsage(provider *ExecutionProvider, models ...string) bool {
	if provider != nil && provider.Record.Platform == capability.PlatformGrok {
		return true
	}
	for _, model := range models {
		normalized := strings.ToLower(strings.TrimSpace(model))
		if separator := strings.LastIndex(normalized, "/"); separator >= 0 {
			normalized = strings.TrimSpace(normalized[separator+1:])
		}
		if normalized == "grok" || strings.HasPrefix(normalized, "grok-") {
			return true
		}
	}
	return false
}
