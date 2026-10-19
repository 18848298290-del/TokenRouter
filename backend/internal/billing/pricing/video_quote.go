package pricing

// video_quote.go builds per-second public quotes from the same video labels
// and configured unit selection used by settlement. Each configured resolution
// appears independently, including free tiers. A configured default is carried
// separately because it applies to unlisted dimensions, rather than describing
// a particular output resolution or promising that an upstream supports it.
//
// A selected video card determines the available user prices. Catalog-only
// queries expose the dimensions published for that exact model, and a missing
// price remains unavailable. Both sources are copied into response-owned maps
// before the group multiplier is applied, keeping cached source amounts stable
// for subsequent quotes and charges.

// BuildVideoDisplayPricing returns available video units with the group rate.
// A nonnil resolved value must select video mode. Configured labels use the
// same request-unit resolver as settlement, including aliases and explicit
// default prices; a nil resolution reads only the supplied model catalog.
//
// Missing or invalid amounts are omitted, and a default price alone can make
// the result available. Present zeros survive rate scaling. Callers supply a
// finite nonnegative rate; the returned map and default pointer belong to this
// quote and may be encoded without mutating the catalog or resolved card.
func BuildVideoDisplayPricing(resolved *ResolvedPricing, catalog *CatalogModelPricing, rate float64) (ModelDisplayPricing, bool) {
	result := ModelDisplayPricing{PricingMode: "video", PriceStatus: "priced", VideoPrices: make(map[string]float64)}
	if resolved != nil {
		if resolved.Mode != BillingModeVideo {
			return ModelDisplayPricing{}, false
		}
		for _, tier := range resolved.RequestTiers {
			label, valid := LookupVideoBillingResolution(tier.TierLabel)
			if !valid {
				continue
			}
			if value, found := ResolveRequestUnitPrice(resolved, label, nil); found && validAmount(value) && validAmount(value*rate) {
				result.VideoPrices[label] = value * rate
			}
		}
		if value, found := ResolveRequestUnitPrice(resolved, "", nil); found && validAmount(value) && validAmount(value*rate) {
			scaled := value * rate
			result.VideoPricePerSecond = &scaled
		}
	} else if catalog != nil {
		prices, err := NormalizeVideoPrices(catalog.VideoPrices)
		if err != nil {
			// Reject the whole invalid table so a conflicting alias cannot publish
			// one arbitrary price while settlement reports unavailable catalog pricing.
			return ModelDisplayPricing{}, false
		}
		for label, value := range prices {
			if validAmount(value * rate) {
				result.VideoPrices[label] = value * rate
			}
		}
	}
	return result, len(result.VideoPrices) > 0 || result.VideoPricePerSecond != nil
}
