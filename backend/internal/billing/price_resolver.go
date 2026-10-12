package billing

import (
	"context"
	"strings"

	purepricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

const (
	PricingSourceCatalog  = purepricing.PricingSourceCatalog
	PricingSourceUnpriced = purepricing.PricingSourceUnpriced
)

// ResolvedPricing aliases the result owned by the pricing package.
type ResolvedPricing = purepricing.ResolvedPricing

// PricingInput identifies the model and optional group whose prices to resolve.
type PricingInput struct {
	Model   string
	GroupID *int64 // nil skips shared pricing configuration lookup
}

type ConfigPrices interface {
	GetEffectiveConfigModelPricing(context.Context, int64, string) *ModelPricingEntry
}
type ModelIdentity struct {
	Candidates []string
}
type ModelCandidates func(string) ModelIdentity

// PriceResolver reads the group's shared pricing configuration and queries the model catalog as needed.
type PriceResolver struct {
	pricingConfigs ConfigPrices
	calculator     *Calculator
	lookup         ModelCandidates
	observe        func(string, error)
	providerStats  ProviderStatsSource
}

func NewPriceResolver(pricingConfigs ConfigPrices, calculator *Calculator, lookup ModelCandidates, observe func(string, error), stats ...ProviderStatsSource) *PriceResolver {
	if observe == nil {
		observe = func(string, error) {}
	}
	var providerStats ProviderStatsSource
	if len(stats) > 0 {
		providerStats = stats[0]
	}
	return &PriceResolver{pricingConfigs: pricingConfigs, calculator: calculator, lookup: lookup, observe: observe, providerStats: providerStats}
}

// Resolve combines configured and catalog prices with the group's billing settings.
// @project-doc docs/domains/routing_and_billing.md#group_model_pricing
func (r *PriceResolver) Resolve(ctx context.Context, input PricingInput) *ResolvedPricing {
	var configPricing *ModelPricingEntry
	if input.GroupID != nil && r.pricingConfigs != nil {
		configPricing = r.LookupConfigPricingNormalized(ctx, *input.GroupID, input.Model)
	}
	var base *ModelPricing
	source := PricingSourceUnpriced
	if purepricing.PriceCardNeedsBase(configPricing) {
		base, source = r.ResolveBasePricing(input.Model)
	}
	settings := r.BillingSettings(ctx, input.GroupID)
	return purepricing.ResolvePriceCards(configPricing, base, source, settings.LongContextPricingEnabled)
}

// ResolveBasePricing retrieves catalog pricing for the full model name.
func (r *PriceResolver) ResolveBasePricing(model string) (*ModelPricing, string) {
	pricing, err := r.calculator.GetModelPricing(model)
	if err != nil {
		r.observe(model, err)
		return nil, PricingSourceUnpriced
	}
	return pricing, PricingSourceCatalog
}

// LookupConfigPricingNormalized tries the requested name before catalog identity candidates.
// Candidates resolve protocol resource paths for the same model; manual prices can cover uncataloged models.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_metadata_lookup
func (r *PriceResolver) LookupConfigPricingNormalized(ctx context.Context, groupID int64, model string) *ModelPricingEntry {
	if r == nil || r.pricingConfigs == nil {
		return nil
	}
	return LookupPricingForModel(model, func(candidate string) *ModelPricingEntry {
		return r.pricingConfigs.GetEffectiveConfigModelPricing(ctx, groupID, candidate)
	}, r.lookup)
}

// LookupPricingForModel tries exact or wildcard cards for the full requested name before identity candidates.
func LookupPricingForModel(model string, lookup func(string) *ModelPricingEntry, identities ModelCandidates) *ModelPricingEntry {
	if pricing := lookup(model); pricing != nil {
		return pricing
	}
	projection := ModelIdentity{}
	if identities != nil {
		projection = identities(model)
	}
	candidates := projection.Candidates
	for _, candidate := range candidates {
		if strings.EqualFold(candidate, strings.TrimSpace(model)) {
			continue
		}
		if pricing := lookup(candidate); pricing != nil {
			return pricing
		}
	}
	return nil
}

// BillingSettings returns the group's effective settings, or defaults when no settings source is available.
func (r *PriceResolver) BillingSettings(ctx context.Context, groupID *int64) purepricing.BillingSettings {
	if r != nil && groupID != nil {
		if source, ok := r.pricingConfigs.(interface {
			GetEffectiveBillingSettings(context.Context, int64) purepricing.BillingSettings
		}); ok {
			return source.GetEffectiveBillingSettings(ctx, *groupID).Clone()
		}
	}
	return purepricing.DefaultBillingSettings()
}
