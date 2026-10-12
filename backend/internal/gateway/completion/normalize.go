package completion

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

type ServiceTierBillingResolution struct {
	Requested  string // Outbound tier; empty when unspecified.
	Observed   string // Tier declared by upstream; empty when absent.
	Billing    string // Tier used for billing and logs.
	Downgraded bool   // Whether billing uses a lower tier than requested.
}

func ResolveBillingServiceTier(requested, observed string) ServiceTierBillingResolution {
	requested = normalizeBillingServiceTier(requested)
	observed = normalizeBillingServiceTier(observed)
	resolution := ServiceTierBillingResolution{Requested: requested, Observed: observed, Billing: requested}
	if observed == "" || observed == requested {
		return resolution
	}
	observedRank, known := ServiceTierCostRank(observed)
	if !known {
		return resolution
	}
	requestedRank, _ := ServiceTierCostRank(requested)
	if observedRank >= requestedRank {
		return resolution
	}
	resolution.Billing = observed
	resolution.Downgraded = true
	return resolution
}

func ServiceTierCostRank(tier string) (rank int, known bool) {
	switch normalizeBillingServiceTier(tier) {
	case "flex":
		return 0, true
	case "", "default", "standard", "auto", "scale":
		return 1, true
	case "priority", "fast":
		return 2, true
	default:
		return 1, false
	}
}

func applyCacheOverride(usage *TokenUsage, target string) bool {
	projected := protocol.TokenUsage{CacheCreationInputTokens: usage.CacheCreationInputTokens, CacheCreation5mTokens: usage.CacheCreation5mTokens, CacheCreation1hTokens: usage.CacheCreation1hTokens}
	changed := protocol.ApplyCacheTTLOverride(&projected, target)
	usage.CacheCreation5mTokens = projected.CacheCreation5mTokens
	usage.CacheCreation1hTokens = projected.CacheCreation1hTokens
	return changed
}

// normalizeResult resolves image dimensions and the billing tier from request and response observations.
func (s *Recorder) normalizeResult(r *Result, a *ProviderSnapshot, openAI bool, observedProvider *ProviderSnapshot, requestID string) ServiceTierBillingResolution {
	if r == nil {
		return ServiceTierBillingResolution{}
	}
	if r.ImageCount > 0 && (!openAI || r.VideoCount <= 0) {
		input := strings.TrimSpace(r.ImageInputSize)
		if input == "" && strings.TrimSpace(r.ImageSize) != pricing.ImageBillingSize2K {
			input = strings.TrimSpace(r.ImageSize)
		}
		sizes := r.ImageOutputSizes
		if len(sizes) == 0 && strings.TrimSpace(r.ImageOutputSize) != "" {
			sizes = []string{r.ImageOutputSize}
		}
		v := pricing.ResolveImageBillingSize(input, sizes)
		r.ImageSize, r.ImageInputSize, r.ImageOutputSize, r.ImageSizeSource, r.ImageSizeBreakdown = v.BillingSize, v.InputSize, v.OutputSize, v.Source, v.Breakdown
	}
	requested := stringValueOrEmpty(r.ServiceTier)
	if !openAI && requested == "" {
		requested = strings.TrimSpace(r.Usage.Speed)
	}
	resolution := ResolveBillingServiceTier(requested, r.UpstreamResponseServiceTier)
	if openAI {
		resolution = ResolveOpenAIServiceTierBilling(a != nil && a.OAuthLike, requested, r.UpstreamResponseServiceTier)
	}
	if resolution.Downgraded {
		tier := resolution.Billing
		r.ServiceTier = &tier
		if !openAI && r.Usage.Speed != "" {
			r.Usage.Speed = tier
		}
		event := BillingEvent{Kind: "tier_downgrade", Component: "service.gateway", RequestID: strings.TrimSpace(requestID), RequestedTier: resolution.Requested, ObservedTier: resolution.Observed, BilledTier: resolution.Billing}
		if openAI {
			event.Component = "service.openai_gateway"
		}
		if observedProvider != nil {
			event.ProviderID = observedProvider.ID
			event.Platform = observedProvider.Platform
		}
		s.observeEvent(event)
	}
	return resolution
}

// ResolveOpenAIServiceTierBilling ignores OAuth default echoes; other observations can lower the billed tier.
func ResolveOpenAIServiceTierBilling(oauthLike bool, requested, observed string) ServiceTierBillingResolution {
	if oauthLike && CodexOAuthResponseTierIsNonAuthoritative(observed) {
		return ServiceTierBillingResolution{Requested: normalizeBillingServiceTier(requested), Observed: normalizeBillingServiceTier(observed), Billing: normalizeBillingServiceTier(requested)}
	}
	return ResolveBillingServiceTier(requested, observed)
}

func CodexOAuthResponseTierIsNonAuthoritative(observed string) bool {
	return normalizeBillingServiceTier(observed) == "default"
}
