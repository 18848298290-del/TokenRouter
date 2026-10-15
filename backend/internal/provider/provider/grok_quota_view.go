package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_quota_view.go connects subscription and time parsers to the provider
// quota display. Grok quota display interprets saved subscription JWTs, plan
// aliases and free rolling-token allowance alongside observation timestamps.
// The view binds those platform parsers to the provider display rules, so
// later quota reads use the same reauthorization and tier interpretation as
// provider admission.

// NewGrokQuotaView binds platform tier and time parsers for provider quota display.
func NewGrokQuotaView() *provider.GrokQuotaView {
	return &provider.GrokQuotaView{
		FreeTokenLimit:      grok.GrokFreeRolling24hTokenLimit,
		NeedsReauth:         provider.GrokNeedsReauth,
		JWTSubscriptionTier: grok.SubscriptionTierFromJWT,
		CanonicalPlan:       grok.CanonicalGrokPlan,
		ParseTime:           provider.ParseUsageTime,
	}
}
