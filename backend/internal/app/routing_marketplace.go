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

// routing_marketplace.go assembles marketplace model display with shared
// routing, capacity, pricing, and public statistics. Model presentation also
// receives current attributes and availability probes. Its statistics adapter
// exposes dashboard token/user totals, while price and modality calls delegate
// to shared billing components so public presentation uses the assembled
// pricing readers.

// marketplacePrices adapts billing quotes and model modalities for marketplace
// presentation.
type marketplacePrices struct {
	resolver   *billing.PriceResolver // group model quotes
	calculator *billing.Calculator    // catalog model modalities
}

// marketplaceStats narrows public dashboard counts for the marketplace
// homepage.
type marketplaceStats struct {
	source *usage.DashboardService // public dashboard totals
}

// provideMarketplace binds groups, settings, requestable models, and
// price/capacity presentation. cfg, catalogue, and attributes are required by
// construction; the marketplace retains the supplied timezone and current
// model-attribute resolver for later queries.
func provideMarketplace(groups *routingpostgres.GroupStore, store *settings.Store, catalogue *routing.RequestableCatalogue, prices *billing.PriceResolver, calculator *billing.Calculator, capacity *routing.CapacityService, availability routing.GroupAvailabilityProbeRepository, cfg *config.Config, attributes *routing.ModelAttributeService) *routing.Marketplace {
	options := routing.MarketplaceOptions{Attributes: attributes.ResolveMarketplaceGroups, Timezone: cfg.Timezone, Now: time.Now, Warn: slog.Warn}
	return routing.NewMarketplace(groups, store, catalogue, catalogue.Resolver, marketplacePrices{prices, calculator}, capacity, availability, options)
}

// PublicStats returns dashboard token and user counts for public marketplace
// display.
func (s marketplaceStats) PublicStats(ctx context.Context) (routingdto.ModelMarketplaceStats, error) {
	value, err := s.source.GetPublicDashboardStats(ctx)
	if err != nil {
		return routingdto.ModelMarketplaceStats{}, err
	}
	return routingdto.ModelMarketplaceStats{TodayTokens: value.TodayTokens, TotalTokens: value.TotalTokens, TotalUsers: value.TotalUsers}, nil
}

// Quote delegates the requested group/model and display multipliers to billing
// public pricing.
func (p marketplacePrices) Quote(ctx context.Context, request routing.MarketplaceQuoteRequest) pricing.ModelDisplayPricing {
	return p.resolver.PublicQuote(ctx, billing.PublicQuoteInput{PricingInput: billing.PricingInput{
		Model: request.Model, GroupID: &request.GroupID,
	}, RateMultiplier: request.RateMultiplier, FreeFastApplicable: request.FreeFastApplicable})
}

// GetModelModalities delegates input and output modality reads to the shared
// calculator.
func (p marketplacePrices) GetModelModalities(model string) ([]string, []string) {
	return p.calculator.GetModelModalities(model)
}

// provideMarketplaceHTTP combines the marketplace with narrowed public
// dashboard statistics.
func provideMarketplaceHTTP(core *routing.Marketplace, dashboard *usage.DashboardService) *routinghttp.MarketplaceHandler {
	return routinghttp.NewMarketplaceHandler(core, marketplaceStats{source: dashboard})
}
