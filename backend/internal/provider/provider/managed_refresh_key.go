package provider

import "github.com/TokenFlux/TokenRouter/internal/provider"

// managed_refresh_key.go selects the token-cache namespace for managed
// provider refreshes. Managed refreshes share token-cache namespaces with
// request and background refreshes. Qoder COSY is recognized before the
// platform switch; OpenAI, Gemini, Antigravity, and Grok use their platform
// keys, while remaining records use the Claude token key.

// ManagedRefreshCacheKey selects the token-cache key for a nonnil provider
// record. Qoder COSY takes precedence over the platform value, and unlisted
// platforms use the Claude key shared by administrative, background, and
// request refreshes.
func ManagedRefreshCacheKey(value *provider.Record) string {
	if value.IsQoderCosy() {
		return provider.QoderTokenCacheKey(value)
	}
	switch value.Platform {
	case provider.PlatformOpenAI:
		return provider.OpenAITokenCacheKey(value)
	case provider.PlatformGemini:
		return GeminiTokenCacheKey(value)
	case provider.PlatformAntigravity:
		return provider.AntigravityTokenCacheKey(value)
	case provider.PlatformGrok:
		return provider.GrokTokenCacheKey(value)
	default:
		return provider.ClaudeTokenCacheKey(value)
	}
}
