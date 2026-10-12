package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routinghttp "github.com/TokenFlux/TokenRouter/internal/routing/httpapi"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type marketplacePrices struct {
	resolver   *billing.PriceResolver
	calculator *billing.Calculator
}

// marketplaceStats reads dashboard counts for the public homepage.
type marketplaceStats struct{ source *usage.DashboardService }

// provideMarketplace binds groups, settings, and quotes with model information read at query time.
func provideMarketplace(groups *routingpostgres.GroupStore, store *settings.Store, catalogue *routing.RequestableCatalogue, prices *billing.PriceResolver, calculator *billing.Calculator, capacity *routing.CapacityService, availability routing.GroupAvailabilityProbeRepository, cfg *config.Config, attributes *routing.ModelAttributeService) *routing.Marketplace {
	options := routing.MarketplaceOptions{Attributes: attributes.ResolveGroups, Timezone: cfg.Timezone, Now: time.Now, Warn: slog.Warn}
	return routing.NewMarketplace(groups, store, catalogue, catalogue.Resolver, marketplacePrices{prices, calculator}, capacity, availability, options)
}

func (s marketplaceStats) PublicStats(ctx context.Context) (routingdto.ModelMarketplaceStats, error) {
	value, err := s.source.GetPublicDashboardStats(ctx)
	if err != nil {
		return routingdto.ModelMarketplaceStats{}, err
	}
	return routingdto.ModelMarketplaceStats{TodayTokens: value.TodayTokens, TotalTokens: value.TotalTokens, TotalUsers: value.TotalUsers}, nil
}

func (p marketplacePrices) Quote(ctx context.Context, request routing.MarketplaceQuoteRequest) pricing.ModelDisplayPricing {
	return p.resolver.PublicQuote(ctx, billing.PublicQuoteInput{PricingInput: billing.PricingInput{
		Model: request.Model, GroupID: &request.GroupID,
	}, RateMultiplier: request.RateMultiplier, FreeFastApplicable: request.FreeFastApplicable})
}

func (p marketplacePrices) GetModelModalities(model string) ([]string, []string) {
	return p.calculator.GetModelModalities(model)
}

// provideMarketplaceHTTP binds public statistics to the marketplace handler.
func provideMarketplaceHTTP(core *routing.Marketplace, dashboard *usage.DashboardService) *routinghttp.MarketplaceHandler {
	return routinghttp.NewMarketplaceHandler(core, marketplaceStats{source: dashboard})
}
