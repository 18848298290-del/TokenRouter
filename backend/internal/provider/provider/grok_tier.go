package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_tier.go supplies shared subscription-tier parsing rules for Grok media
// admission and quota display. The provider core receives JWT tier extraction,
// canonical tier names and free-window detection from the platform
// implementation, allowing those consumers to interpret stored credentials and
// quota observations consistently.

// GrokTierRules supplies platform tier parsers to provider admission and quota
// consumers.
func GrokTierRules() provider.GrokTierRules {
	return provider.GrokTierRules{
		SubscriptionTierFromJWT:   grok.SubscriptionTierFromJWT,
		NormalizeSubscriptionTier: grok.NormalizeSubscriptionTier,
		IsFreeRollingTokenLimit:   grok.IsGrokFreeRolling24hTokenLimit,
	}
}
