package provider

import (
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

type PriceModel struct{ Model, Provider, Mode, BillingMode string }

// PricingView 固定一次发布的价格与型号索引。单型号返回副本，枚举只传递字符串。
type PricingView struct {
	snapshot *Service
	models   []PriceModel
	Status   AttributeSnapshot
}

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

func (v PricingView) Models() iter.Seq[PriceModel] {
	return func(yield func(PriceModel) bool) {
		for _, model := range v.models {
			if !yield(model) {
				return
			}
		}
	}
}

func (v PricingView) GetModelPricing(model string) *CatalogModelPricing {
	if v.snapshot == nil {
		return nil
	}
	return pricing.CloneCatalogPrice(v.snapshot.GetModelPricing(model))
}

func (v PricingView) ForceUpdate() error {
	return fmt.Errorf("a fixed price view cannot update the catalog")
}

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
		if strings.Contains(item.Model, "grok-imagine-video") {
			item.Mode = "video"
		}
		raw := prices[name]
		if raw == nil {
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
