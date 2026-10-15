package provider

import (
	"strings"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_cache_policy.go resolves Free-tier client-tool caching policy and
// applies its request route.
// Known Free Grok OAuth targets can route function tools through the upstream
// cacheable mixed-tool path. The provider override accepts booleans, with
// invalid configured values disabling client-only caching; tier classification
// includes inferred Free evidence and differs from the explicit-tier soft
// quota gate.
//
// An absent override enables client-tool caching for a known Free provider,
// whereas a present value of the wrong type disables it and is reported as
// explicit. The two returned booleans preserve that distinction for callers.
// Empty cache identities leave request bytes unchanged before the upstream
// routing helper is invoked, so the route requires both provider eligibility
// and a usable identity.

// grokClientToolCacheOptInExtraKey names the provider Extra override
// controlling client-only tool cache routing for known Free OAuth targets.
const grokClientToolCacheOptInExtraKey = "grok_client_tool_cache_enabled"

// ApplyGrokFreeMessagesFunctionToolCacheRoute prepares the Free-provider
// tool-cache route with function-search support enabled. Client-only tools
// follow the provider cache policy, while the upstream helper receives both
// forwarded and original-intent bodies to decide applicability.
func ApplyGrokFreeMessagesFunctionToolCacheRoute(body, intentSourceBody []byte, provider *ExecutionProvider, cacheIdentity string) ([]byte, error) {
	allowPureClientTools, _ := GrokClientToolCacheProviderPolicy(provider)
	return ApplyGrokFreeToolCacheRoute(body, intentSourceBody, provider, cacheIdentity, allowPureClientTools, true)
}

// GrokClientToolCacheProviderPolicy returns client-tool cache enablement and
// whether a recognized Free provider configured an override. An absent
// override enables caching; a non-boolean override returns disabled with
// explicit=true, and other providers return false for both results.
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

// ApplyGrokFreeToolCacheRoute delegates cache-route encoding with Free-tier
// eligibility and the supplied tool permissions. Blank cacheIdentity returns
// body unchanged; the upstream helper decides whether forwarded and
// original-intent tool sets qualify.
func ApplyGrokFreeToolCacheRoute(body, intentSourceBody []byte, provider *ExecutionProvider, cacheIdentity string, allowPureClientTools, allowFunctionSearch bool) ([]byte, error) {
	if strings.TrimSpace(cacheIdentity) == "" {
		return body, nil
	}
	return grok.ApplyGrokFreeToolCacheRoute(body, intentSourceBody, IsKnownGrokFreeProvider(provider), cacheIdentity, allowPureClientTools, allowFunctionSearch)
}

// IsKnownGrokFreeProvider applies provider tier rules to identify Free OAuth
// targets for cache and media policy. Inferred Free evidence participates,
// while the rolling soft quota gate uses the separate explicit-tier predicate.
func IsKnownGrokFreeProvider(provider *ExecutionProvider) bool {
	return providercore.KnownGrokFreeProvider(ExecutionRecord(provider), provideradapter.GrokTierRules())
}
