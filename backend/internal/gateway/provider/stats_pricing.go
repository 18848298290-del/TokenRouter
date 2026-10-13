package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Provider cost statistics use the price configuration linked to a group,
// independent of model authorization. The adapter exposes that configuration's
// cost rules to billing and carries a missing configuration through as a nil
// result so the caller can apply its pricing policy.

// ProviderStatsSource adapts group pricing configuration to billing's
// provider-cost rule reader.
type ProviderStatsSource struct {
	// group price configuration reader
	Service *routing.PricingConfigService
}

// ProviderStatsGroup retrieves the price configuration attached to id and
// exposes its provider-statistics rules to billing. The service must be
// configured; missing price configuration returns nil, while lookup errors are
// returned to the caller.
func (s ProviderStatsSource) ProviderStatsGroup(ctx context.Context, id int64) (*billing.ProviderStatsPricingConfig, error) {
	pricingConfig, err := s.Service.GetPricingConfigForGroup(ctx, id)
	if err != nil || pricingConfig == nil {
		return nil, err
	}
	return &billing.ProviderStatsPricingConfig{Rules: pricingConfig.ProviderStatsPricingRules}, nil
}
