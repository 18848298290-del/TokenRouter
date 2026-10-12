package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ProviderStatsSource supplies shared pricing rules for provider cost calculations.
type ProviderStatsSource struct{ Service *routing.PricingConfigService }

func (s ProviderStatsSource) ProviderStatsGroup(ctx context.Context, id int64) (*billing.ProviderStatsPricingConfig, error) {
	pricingConfig, err := s.Service.GetPricingConfigForGroup(ctx, id)
	if err != nil || pricingConfig == nil {
		return nil, err
	}
	return &billing.ProviderStatsPricingConfig{Rules: pricingConfig.ProviderStatsPricingRules}, nil
}
