package billing

import (
	"context"
	"strings"

	purepricing "github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// ProviderStatsPricingConfig contains model-price rules for provider statistics.
type ProviderStatsPricingConfig struct {
	Rules []purepricing.ProviderStatsPricingRule
}
type ProviderStatsSource interface {
	ProviderStatsGroup(context.Context, int64) (*ProviderStatsPricingConfig, error)
}

// ProviderStatsCostInput supplies upstream usage and final service tier; settlement applies the provider multiplier separately.
type ProviderStatsCostInput struct {
	// PreferRequestedModel is supplied by the actual provider's capabilities.
	PreferRequestedModel                       bool
	ProviderID, GroupID                        int64
	UpstreamModel, RequestedModel, MappedModel string
	Tokens                                     UsageTokens
	RequestCount                               int
	ServiceTier, ReasoningEffort               string
}

func (r *PriceResolver) ResolveProviderStats(ctx context.Context, input ProviderStatsCostInput) *float64 {
	if r == nil || r.providerStats == nil || input.UpstreamModel == "" {
		return nil
	}
	configPricing, err := r.providerStats.ProviderStatsGroup(ctx, input.GroupID)
	if err != nil || configPricing == nil {
		return nil
	}
	if cost, handled := purepricing.ResolveProviderStatsOverride(purepricing.ProviderStatsInput{
		Rules: configPricing.Rules, ProviderID: input.ProviderID, GroupID: input.GroupID,
		Models: ProviderStatsRuleModels(input.PreferRequestedModel, input.UpstreamModel, input.RequestedModel, input.MappedModel),
		Tokens: input.Tokens, RequestCount: input.RequestCount,
	}); handled {
		return cost
	}
	if r.calculator == nil {
		return nil
	}
	return r.calculator.ModelFileStatsCost(input.UpstreamModel, input.Tokens, input.ServiceTier, input.ReasoningEffort)
}

// ModelFileStatsCost calculates catalog cost with user billing rules and no shared configuration overrides.
func (s *Calculator) ModelFileStatsCost(model string, tokens UsageTokens, serviceTier, effort string) *float64 {
	breakdown, err := s.CalculateCostUnified(CostInput{Model: model, Tokens: tokens, RateMultiplier: 1, ServiceTier: purepricing.NormalizeBillingServiceTier(serviceTier), ReasoningEffort: effort})
	if err != nil || breakdown == nil || breakdown.TotalCost <= 0 {
		return nil
	}
	return &breakdown.TotalCost
}

func ProviderStatsRuleModels(preferRequested bool, upstreamModel, requestedModel string, groupMappedModel ...string) []string {
	upstreamModel = strings.TrimSpace(upstreamModel)
	requestedModel = strings.TrimSpace(requestedModel)
	mappedModel := ""
	if len(groupMappedModel) > 0 {
		mappedModel = strings.TrimSpace(groupMappedModel[0])
	}
	if !preferRequested || requestedModel == "" ||
		(requestedModel == upstreamModel && (mappedModel == "" || mappedModel == requestedModel)) {
		if upstreamModel == "" {
			return nil
		}
		return []string{upstreamModel}
	}
	models := []string{requestedModel}
	models = append(models, mappedModel)
	models = append(models, upstreamModel)
	return purepricing.UniqueNonEmptyProviderStatsModels(models)
}
