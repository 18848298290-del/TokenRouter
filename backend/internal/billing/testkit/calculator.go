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

// Billing regression tests need known historical token amounts alongside media
// fixture prices. An omitted catalog and price map select those embedded
// fixtures, while a supplied catalog keeps its existing full-name entries
// ahead of supplied calculation-price conversions.
//
// The fixture converts normalized token amounts into catalog fields so tests
// exercise the production catalog resolution path. Existing snapshot entries
// are retained, absent non-nil prices fill new names, and the default fixture
// then overlays parsed media entries. Construction returns a calculator with
// local-calendar time and the production pricing location loader; it starts no
// refresh worker.

var (
	// historicalPriceData provides embedded normalized token-price amounts for
	// offline regression fixtures, decoded afresh so each test can change its
	// inputs independently.
	//
	//go:embed testdata/historical_prices.json
	historicalPriceData []byte

	// mediaPriceData provides embedded media catalog entries for the default
	// fixture, parsed by production pricing rules before media lookup and
	// unit-charge tests use them.
	//
	//go:embed testdata/media_prices.json
	mediaPriceData []byte
)

// HistoricalPrices decodes a fresh mutable historical token-price map for each
// call. Embedded JSON decode failure panics because the fixture cannot supply
// valid regression inputs; callers can change the returned price records
// independently of later calls.
func HistoricalPrices() map[string]*pricing.ModelPricing {
	var prices map[string]*pricing.ModelPricing
	if err := json.Unmarshal(historicalPriceData, &prices); err != nil {
		panic(err)
	}
	return prices
}

// Calculator builds a production calculator from the supplied catalog and
// normalized prices. When both are nil, it loads historical token and media
// fixtures; a non-nil price map fills only absent catalog names, skipping nil
// entries.
//
// Normalized prices are converted into catalog fields, including priority,
// cache TTL, media-token and rule modifiers. Existing catalog names retain
// precedence, and the baseline path overlays its parsed media entries before
// creating a snapshot-backed service. The returned calculator uses
// local-calendar time and the production time-zone loader. Supplied modifier
// pointers remain shared, so callers keep them unchanged during calculation.
// Embedded fixture decode failures panic because no valid fixture can be built.
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
		// Retain catalog entries for existing full names; converting only absent
		// names keeps supplied normalized fixtures from replacing that snapshot's
		// model amounts.
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
			// A split-cache fixture supplies its five-minute amount as the aggregate
			// catalog write bucket, while its hourly amount and presence flag retain
			// TTL selection.
			if price.SupportsCacheBreakdown {
				data[name].CacheCreationInputTokenCost = price.CacheCreation5mPrice
			}
		}
		// The default fixture overlays its parsed media entries after historical
		// token conversion, giving those embedded media names their declared unit
		// prices.
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
