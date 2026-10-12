package pricing

import (
	"fmt"
	"strings"
)

// ResolveModelPricing converts a catalog entry into token billing prices.
func ResolveModelPricing(model string, catalogPrice *CatalogModelPricing) (*ModelPricing, error) {
	// Normalize case for model names in diagnostics.
	model = strings.ToLower(model)
	if catalogPrice == nil || catalogPrice.Source == "unpriced" || catalogPrice.TokenPricingAbsent {
		return nil, fmt.Errorf("%w for model: %s", ErrModelPricingUnavailable, model)
	}

	catalogPricing := catalogPrice
	rules := catalogPricing.Clone()
	inclusiveThreshold := strings.EqualFold(catalogPricing.Provider, "xai")
	var contextPrices []ContextModelPrice
	for _, tier := range catalogPricing.ContextPrices {
		if tier.Pricing == nil {
			continue
		}
		price, err := ResolveModelPricing(model, tier.Pricing)
		if err == nil {
			threshold := tier.Threshold
			// Internal tiers use (min, max]; xAI switches at the threshold, so convert it to the preceding tier's maximum.
			if inclusiveThreshold && threshold > 0 {
				threshold--
			}
			contextPrices = append(contextPrices, ContextModelPrice{Threshold: threshold, Pricing: price})
		}
	}
	// An explicit 1h cache price enables TTL billing, including an explicit zero.
	// Legacy entries infer TTL support when the 1h price exceeds the 5m price.
	price5m := catalogPricing.CacheCreationInputTokenCost
	price1h := catalogPricing.CacheCreationInputTokenCostAbove1hr
	enableBreakdown := price1h > 0 && price1h > price5m || catalogPricing.CacheCreation1hPricePresent
	return &ModelPricing{
		FastMultiplier:                     rules.FastMultiplier,
		FlexMultiplier:                     rules.FlexMultiplier,
		TimePricing:                        rules.TimePricing,
		CatalogSource:                      catalogPricing.Source,
		CacheCreationPriceExplicit:         catalogPricing.Source != "" && catalogPricing.CacheCreationPricePresent && catalogPricing.CacheCreationInputTokenCost == 0,
		PriorityInputPresent:               catalogPricing.PriorityInputPresent,
		PriorityOutputPresent:              catalogPricing.PriorityOutputPresent,
		PriorityCacheReadPresent:           catalogPricing.PriorityCacheReadPresent,
		PriorityCacheWritePresent:          catalogPricing.PriorityCacheWritePresent,
		ContextPrices:                      contextPrices,
		InputPricePerToken:                 catalogPricing.InputCostPerToken,
		InputPricePerTokenPriority:         catalogPricing.InputCostPerTokenPriority,
		OutputPricePerToken:                catalogPricing.OutputCostPerToken,
		OutputPricePerTokenPriority:        catalogPricing.OutputCostPerTokenPriority,
		CacheCreationPricePerToken:         catalogPricing.CacheCreationInputTokenCost,
		CacheCreationPricePerTokenPriority: catalogPricing.CacheCreationInputTokenCostPriority,
		CacheReadPricePerToken:             catalogPricing.CacheReadInputTokenCost,
		CacheReadPricePerTokenPriority:     catalogPricing.CacheReadInputTokenCostPriority,
		CacheCreation5mPrice:               price5m,
		CacheCreation1hPrice:               price1h,
		SupportsCacheBreakdown:             enableBreakdown,
		SupportsServiceTier:                catalogPricing.SupportsServiceTier,
		// xAI enters the higher tier at the threshold; other providers require usage above it.
		LongContextThresholdInclusive: inclusiveThreshold,
		LongContextInputThreshold:     catalogPricing.LongContextInputTokenThreshold,
		LongContextInputMultiplier:    catalogPricing.LongContextInputCostMultiplier,
		LongContextOutputMultiplier:   catalogPricing.LongContextOutputCostMultiplier,
		ImageInputPricePerToken:       catalogPricing.InputCostPerImageToken,
		ImageOutputPricePerToken:      catalogPricing.OutputCostPerImageToken,
		ImageOutputPriceExplicit:      catalogPricing.ImageOutputPricePresent,
		MaxReasoningEffortMultiplier:  rules.MaxReasoningEffortMultiplier,
	}, nil
}
