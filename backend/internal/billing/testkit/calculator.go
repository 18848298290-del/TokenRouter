package testkit

import (
	_ "embed"
	"encoding/json"
	"maps"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	billingadapter "github.com/TokenFlux/TokenRouter/internal/billing/provider"
	catalogprovider "github.com/TokenFlux/TokenRouter/internal/modelcatalog/provider"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

var (
	// historicalPriceData supplies fixed catalog inputs for regression tests.
	//
	//go:embed testdata/historical_prices.json
	historicalPriceData []byte

	// mediaPriceData supplies fixed catalog inputs for media pricing tests.
	//
	//go:embed testdata/media_prices.json
	mediaPriceData []byte
)

// HistoricalPrices returns a fresh catalog fixture that callers can modify independently.
func HistoricalPrices() map[string]*pricing.ModelPricing {
	var prices map[string]*pricing.ModelPricing
	if err := json.Unmarshal(historicalPriceData, &prices); err != nil {
		panic(err)
	}
	return prices
}

// Calculator constructs a billing calculator from the test catalog and prices.
func Calculator(catalog *catalogprovider.Service, prices map[string]*pricing.ModelPricing) *billing.Calculator {
	useBaseline := prices == nil && catalog == nil
	if useBaseline {
		prices = HistoricalPrices()
	}
	if prices != nil {
		data := map[string]*pricing.CatalogModelPricing{}
		if catalog != nil {
			data = catalog.Snapshot().Data
		}
		if data == nil {
			data = map[string]*pricing.CatalogModelPricing{}
		}
		for name, price := range prices {
			if price == nil {
				continue
			}
			if _, exists := data[name]; exists {
				continue
			}
			data[name] = &pricing.CatalogModelPricing{
				InputCostPerToken: price.InputPricePerToken, OutputCostPerToken: price.OutputPricePerToken,
				CacheReadInputTokenCost: price.CacheReadPricePerToken, CacheCreationInputTokenCost: price.CacheCreationPricePerToken,
				InputCostPerTokenPriority: price.InputPricePerTokenPriority, OutputCostPerTokenPriority: price.OutputPricePerTokenPriority,
				CacheReadInputTokenCostPriority: price.CacheReadPricePerTokenPriority, CacheCreationInputTokenCostPriority: price.CacheCreationPricePerTokenPriority,
				CacheCreationInputTokenCostAbove1hr: price.CacheCreation1hPrice,
				CacheCreation1hPricePresent:         price.SupportsCacheBreakdown,
				SupportsServiceTier:                 price.SupportsServiceTier,
				LongContextInputTokenThreshold:      price.LongContextInputThreshold,
				LongContextInputCostMultiplier:      price.LongContextInputMultiplier, LongContextOutputCostMultiplier: price.LongContextOutputMultiplier,
				InputCostPerImageToken: price.ImageInputPricePerToken, OutputCostPerImageToken: price.ImageOutputPricePerToken,
				CatalogRules: pricing.CatalogRules{FastMultiplier: price.FastMultiplier, FlexMultiplier: price.FlexMultiplier, MaxReasoningEffortMultiplier: price.MaxReasoningEffortMultiplier, TimePricing: price.TimePricing},
			}
			if price.SupportsCacheBreakdown {
				data[name].CacheCreationInputTokenCost = price.CacheCreation5mPrice
			}
		}
		if useBaseline {
			entries, err := pricing.DecodeCatalogEntries(mediaPriceData)
			if err != nil {
				panic(err)
			}
			media, _, err := pricing.ParsePricingEntries(entries)
			if err != nil {
				panic(err)
			}
			maps.Copy(data, media)
		}
		catalog = catalogprovider.NewServiceFromSnapshot(catalogprovider.Options{}, nil, catalogprovider.Snapshot{Data: data})
	}
	var source billing.PriceCatalog
	if catalog != nil {
		source = catalog
	}
	return billing.NewCalculator(source, billing.CalculatorOptions{
		Now:          timezone.NewCalendar(time.Local).Now,
		LoadLocation: billingadapter.LoadPricingLocation,
	})
}
