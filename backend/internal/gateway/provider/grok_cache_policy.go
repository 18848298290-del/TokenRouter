package provider

import (
	"strings"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

const grokClientToolCacheOptInExtraKey = "grok_client_tool_cache_enabled"

// ApplyGrokFreeMessagesFunctionToolCacheRoute enables the cacheable mixed-tool route
// for known Free providers. Client-only tools use it by default; providers can opt out
// when adding native search tools would change the intended behavior (#4486).
func ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentSourceBody []byte, provider *ExecutionProvider, cacheIdentity string) ([]byte, error) {
	allowPureClientTools, _ := GrokClientToolCacheProviderPolicy(provider)
	return ApplyGrokFreeToolCacheRoute(body, intentSourceBody, provider, cacheIdentity, allowPureClientTools, true)
}

// GrokClientToolCacheProviderPolicy accepts JSON booleans and defaults to enabled
// for known Free OAuth providers. Paid, API Key, and unknown providers are disabled.
func GrokClientToolCacheProviderPolicy(provider *ExecutionProvider) (enabled, explicit bool) {
	if !IsKnownGrokFreeProvider(provider) {
		return false, false
	}
	if provider.Record.Extra == nil {
		return true, false
	}
	value, exists := provider.Record.Extra[grokClientToolCacheOptInExtraKey]
	if !exists {
		return true, false
	}
	enabled, valid := value.(bool)
	if !valid {
		return false, true
	}
	return enabled, true
}

func ApplyGrokFreeToolCacheRoute(body, intentSourceBody []byte, provider *ExecutionProvider, cacheIdentity string, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	if strings.TrimSpace(cacheIdentity) == "" {
		return body, nil
	}
	return grok.ApplyGrokFreeToolCacheRoute(body, intentSourceBody, IsKnownGrokFreeProvider(provider), cacheIdentity, allowPureClientTools, allowFunctionSearch)
}

// IsKnownGrokFreeProvider identifies Free providers for cache routing and media blocking.
// Its inferred-tier matching is broader than the soft gate that requires an explicit Free tier.
func IsKnownGrokFreeProvider(provider *ExecutionProvider) bool {
	return providercore.KnownGrokFreeProvider(ExecutionRecord(provider), provideradapter.GrokTierRules())
}
