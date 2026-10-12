package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// NewGrokQuotaView binds platform tier parsing for provider quota display.
func NewGrokQuotaView() *provider.GrokQuotaView {
	return &provider.GrokQuotaView{
		FreeTokenLimit:      grok.GrokFreeRolling24hTokenLimit,
		NeedsReauth:         provider.GrokNeedsReauth,
		JWTSubscriptionTier: grok.SubscriptionTierFromJWT,
		CanonicalPlan:       grok.CanonicalGrokPlan,
		ParseTime:           provider.ParseUsageTime,
	}
}
