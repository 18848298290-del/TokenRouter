package selection

import (
	"strings"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// A Grok quota observation can block one model while leaving the provider's
// other models available. Selection resolves the provider's scheduling model
// before reading that block, so client aliases and wildcard mappings reach the
// cooldown's stored identity.

// filterGrokModelQuotaBlockedProviders returns candidates whose canonical
// scheduling model has no active quota block at now. Empty candidates or a
// blank model reuse the input slice; other results allocate slice storage while
// provider records retain their referenced data.
func filterGrokModelQuotaBlockedProviders(providers []gatewayprovider.ExecutionProvider, model string, now time.Time) []gatewayprovider.ExecutionProvider {
	if len(providers) == 0 || strings.TrimSpace(model) == "" {
		return providers
	}
	out := make([]gatewayprovider.ExecutionProvider, 0, len(providers))
	for i := range providers {
		upstreamModel := gatewayprovider.ExecutionModelPolicy(&providers[i]).CanonicalSchedulingModel(model)
		if providercore.IsGrokModelQuotaBlocked(providers[i].Record.ID, upstreamModel, now) {
			continue
		}
		out = append(out, providers[i])
	}
	return out
}
