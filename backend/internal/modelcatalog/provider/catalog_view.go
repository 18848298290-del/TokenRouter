package provider

import (
	"fmt"
	"iter"
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// catalog_view.go captures fixed pricing views and classifies model identities
// for administrative listings.
// Price listings include attribute-only catalog identities as well as
// supplemented price records. Pricing fields supply each identity's provider
// and mode when available. Billing classification also reads the pricing
// package's resource-path candidates when an exact price entry is missing.
//
// A PricingView captures the price map, model rows, and status while holding
// the service read lock. Updates replace the published data, leaving existing
// views usable for one administrative query. Lookup copies mutable price values
// for its caller, enumeration passes string values, and ForceUpdate rejects
// attempts to refresh this fixed publication.

// PriceModel identifies a listed model's service and billing classification.
type PriceModel struct {
	// Model and Provider identify the listed record. Mode uses price metadata
	// when present; absent pricing metadata retains an unknown operation mode.
	// BillingMode combines that mode and known price dimensions
	// for the administrative listing.
	Model, Provider, Mode, BillingMode string // complete query identity, catalog provider identity, declared pricing mode, derived billing classification
}

// PricingView fixes prices and the model index from one publication. Model
// lookups return copies; enumeration returns strings.
type PricingView struct {
	// snapshot, models, and Status are captured under one service read lock.
	// Their data belongs to that capture; later publications replace the maps
	// and model rows while existing views retain their referenced data.
	snapshot *Service          // retained price lookup data
	models   []PriceModel      // ordered model classification rows
	Status   AttributeSnapshot // captured publication update status
}

// PricingView captures fixed price and model data with the corresponding
// administrative status. The retained maps remain read-only, and each price
// lookup copies its selected value before returning it.
func (s *Service) PricingView() PricingView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	version := s.localHash
	if s.modelCatalog != nil {
		version = s.modelCatalog.Version
	}
	return PricingView{
		snapshot: &Service{options: s.options, modelCatalog: s.modelCatalog, pricingData: s.pricingData},
		models:   s.priceModels,
		Status:   AttributeSnapshot{Version: version, LastUpdated: s.lastUpdated, LastError: s.lastCatalogError},
	}
}

// Models yields retained model classifications in sorted identity order.
// A zero view yields no rows, and iteration stops when the consumer returns
// false; yielded fields contain immutable string values.
func (v PricingView) Models() iter.Seq[PriceModel] {
	return func(yield func(PriceModel) bool) {
		for _, model := range v.models {
			if !yield(model) {
				return
			}
		}
	}
}

// GetModelPricing resolves a model against the retained price data and
// copies the result for caller mutation. A zero view returns nil; unresolved
// prices keep the lookup's unpriced marker when supplied.
func (v PricingView) GetModelPricing(model string) *CatalogModelPricing {
	if v.snapshot == nil {
		return nil
	}
	return pricing.CloneCatalogPrice(v.snapshot.GetModelPricing(model))
}

// ForceUpdate reports that this fixed view cannot refresh its publication.
// Call Service.ForceUpdate to publish updated catalog data, then capture a
// new view for queries using that publication.
func (v PricingView) ForceUpdate() error {
	return fmt.Errorf("a fixed price view cannot update the catalog")
}

// buildPriceModels combines catalog identities and nonnil prices into
// sorted administrative model rows. A price record supplies provider and mode
// in preference to the catalog's provider, while attribute-only identities
// remain available for classification.
//
// Missing exact prices try the pricing package's identity candidates before
// billing mode is derived from explicit modes and price dimensions. Model
// enumeration is independent of token price availability, allowing media and
// attribute-only records to appear in the same fixed price listing.
func buildPriceModels(catalog *modelcatalog.Catalog, prices map[string]*CatalogModelPricing) []PriceModel {
	entries := map[string]PriceModel{}
	if catalog != nil {
		for model, entry := range catalog.Entries {
			entries[model] = PriceModel{Model: model, Provider: entry.Provider}
		}
	}
	for model, price := range prices {
		if price != nil {
			entries[model] = PriceModel{Model: model, Provider: price.Provider, Mode: price.Mode}
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	models := make([]PriceModel, 0, len(names))
	for _, name := range names {
		item := entries[name]
		raw := prices[name]
		if raw == nil {
			// Pricing candidate rules can reduce resource-shaped spellings.
			// Use their candidates for billing classification while retaining
			// the displayed model's complete lookup name.
			for _, candidate := range pricing.BuildModelIdentityCandidates(name) {
				if raw = prices[candidate]; raw != nil {
					break
				}
			}
		}
		item.BillingMode = pricing.DefaultBillingMode(item.Model, item.Mode, raw)
		models = append(models, item)
	}
	return models
}
