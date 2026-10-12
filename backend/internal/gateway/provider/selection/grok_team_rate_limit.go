package selection

import (
	"strings"
	"time"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

func isGrokTeamModelRateLimited(provider *gatewayprovider.ExecutionProvider, model string, now time.Time) bool {
	return providercore.IsGrokTeamModelRateLimited(gatewayprovider.ExecutionRecord(provider), model, now)
}

// filterGrokTeamModelRateLimitedProviders removes candidates whose team is cooling down for the mapped model.
// Providers without a team ID pass. Filtering reuses the input slice backing array.
func filterGrokTeamModelRateLimitedProviders(providers []gatewayprovider.ExecutionProvider, model string, now time.Time) []gatewayprovider.ExecutionProvider {
	if len(providers) == 0 || strings.TrimSpace(model) == "" {
		return providers
	}
	out := providers[:0]
	kept := false
	for i := range providers {
		upstreamModel := gatewayprovider.ExecutionModelPolicy(&providers[i]).CanonicalSchedulingModel(model)
		if isGrokTeamModelRateLimited(&providers[i], upstreamModel, now) {
			continue
		}
		out = append(out, providers[i])
		kept = true
	}
	if !kept && len(out) == 0 {
		return nil
	}
	return out
}
