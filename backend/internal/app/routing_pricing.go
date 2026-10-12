package app

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	pricingprovider "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
)

func providePricingConfigService(repo *routingpostgres.PricingConfigStore, groups *routingpostgres.GroupStore, invalidator apikey.APIKeyAuthCacheInvalidator) *routing.PricingConfigService {
	return routing.NewPricingConfigService(repo, invalidator, routing.PricingConfigOptions{Warn: slog.Warn, Now: time.Now, LoadLocation: pricingprovider.LoadPricingLocation, ReadGroup: func(ctx context.Context, id int64) (*routing.Group, error) {
		access, ok := apikey.AccessSnapshotFromContext(ctx)
		if ok {
			key := access.KeyView()
			if key != nil && key.Group != nil && key.Group.ID == id && key.Group.Hydrated {
				return key.Group, nil
			}
		}
		return groups.GetByIDLite(ctx, id)
	}})
}

func providePricingCatalog(calculator *billing.Calculator, prices *catalogprovider.Service) *routing.PricingCatalog {
	query := func(input routing.DefaultPricingQuery) routing.DefaultPricingPage {
		view := prices.PricingView()
		frozen := calculator.WithPriceCatalog(view)
		result := routing.DefaultPricingPage{Platforms: []string{}, DefaultPricingSnapshot: routing.DefaultPricingSnapshot{
			UpdatedAt: view.Status.LastUpdated, Version: view.Status.Version, LastError: view.Status.LastError, Prices: []pricing.DefaultModelPrice{},
		}}
		platforms := map[string]bool{}
		search, filterPlatform := strings.ToLower(strings.TrimSpace(input.Search)), strings.ToLower(strings.TrimSpace(input.Platform))
		for item := range view.Models() {
			platform := item.Provider
			switch platform {
			case "xai":
				platform = "grok"
			case "moonshot":
				platform = "kimi"
			case "":
				platform = "other"
			}
			platforms[platform] = true
			if filterPlatform != "" && platform != filterPlatform || search != "" && !strings.Contains(strings.ToLower(item.Model), search) {
				continue
			}
			if input.Mode != "" && item.BillingMode != input.Mode {
				continue
			}
			position := result.Total
			result.Total++
			if position < max(0, input.Offset) || input.Limit > 0 && len(result.Prices) >= input.Limit {
				continue
			}
			row := frozen.DefaultModelPrice(item.Model, platform, item.Mode)
			result.Prices = append(result.Prices, row)
		}
		for platform := range platforms {
			result.Platforms = append(result.Platforms, platform)
		}
		sort.Strings(result.Platforms)
		return result
	}
	return &routing.PricingCatalog{Prices: calculator, Update: calculator.ForceUpdatePricing, Query: query, Snapshot: func() routing.DefaultPricingSnapshot {
		return query(routing.DefaultPricingQuery{}).DefaultPricingSnapshot
	}}
}
