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

// routing_pricing.go constructs group pricing configuration and default-price
// queries over a captured catalog view. A default-price response includes
// catalog status and price rows from the same published view. The query freezes
// that view into a calculator before iterating, so catalog replacement during
// the query cannot combine a newer status with rows calculated from another
// view. Group pricing separately reuses a matching hydrated authorization group
// before storage lookup.
//
// Platform names are normalized for display and collected across the catalog
// before filtering. Search, platform, and billing-mode filters decide the
// matching total; pagination limits only returned rows. The status snapshot
// includes that view's version, update time, and error, and an unfiltered
// snapshot uses the same query path.

// providePricingConfigService binds stored price rules, invalidation, and group
// reads. The read callback prefers the authenticated Key's group only when its
// ID matches and Hydrated is true; otherwise it loads the requested lightweight
// routing group.
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

// providePricingCatalog constructs default-price queries with one catalog view
// per invocation. It binds a calculator to that view before selecting rows, and
// returns its status with matching totals and price values.
//
// Search and platform text are trimmed and lowercased; billing mode is matched
// as supplied. Negative offsets act as zero and nonpositive limits leave rows
// unbounded. Platforms includes normalized names from the whole view, while
// Total counts filtered matches before pagination and Snapshot requests the
// unfiltered price list.
func providePricingCatalog(calculator *billing.Calculator, prices *catalogprovider.Service) *routing.PricingCatalog {
	query := func(input routing.DefaultPricingQuery) routing.DefaultPricingPage {
		// Capture status and freeze price lookup from the same published view.
		// Iterating models afterward then keeps row calculations tied to that response's catalogue version.
		view := prices.PricingView()
		frozen := calculator.WithPriceCatalog(view)
		result := routing.DefaultPricingPage{Platforms: []string{}, DefaultPricingSnapshot: routing.DefaultPricingSnapshot{
			UpdatedAt: view.Status.LastUpdated, Version: view.Status.Version, LastError: view.Status.LastError, Prices: []pricing.DefaultModelPrice{},
		}}
		platforms := map[string]bool{}
		search, filterPlatform := strings.ToLower(strings.TrimSpace(input.Search)), strings.ToLower(strings.TrimSpace(input.Platform))
		for item := range view.Models() {
			platform := item.Provider
			// Catalogue provider names differ from displayed routing platforms for
			// xAI, Moonshot, and unnamed entries. Normalize them before filtering and collecting the platform choices.
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
			// Count every filtered match before pagination so Total describes the
			// full result set even after the returned page has reached its row limit.
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
