package pricing

// ResolveConfiguredPricing applies a card's intervals, default prices, and multipliers without modifying the card.
func ResolveConfiguredPricing(config *ModelPricingEntry, base *ModelPricing, source string) *ResolvedPricing {
	mode := config.BillingMode
	if mode == "" {
		mode = BillingModeToken
	}
	resolved := &ResolvedPricing{Mode: mode, Source: source, ConfigPricing: config}
	if mode == BillingModePerRequest || mode == BillingModeImage || mode == BillingModeVideo {
		ApplyRequestTierOverrides(config, resolved)
		ApplyResolvedPriceMultiplier(resolved, config)
		return resolved
	}
	resolved.BasePricing = base
	resolved.SupportsCacheBreakdown = resolved.BasePricing != nil && resolved.BasePricing.SupportsCacheBreakdown
	resolved.SupportsServiceTier = resolved.BasePricing != nil && resolved.BasePricing.SupportsServiceTier
	ApplyTokenOverrides(config, resolved)
	ApplyResolvedPriceMultiplier(resolved, config)
	ApplyResolvedFastModeMultiplier(resolved, config)
	return resolved
}

// PriceCardOverrides reports whether a card supplies explicit prices or valid token intervals.
func PriceCardOverrides(card *ModelPricingEntry) bool {
	return card != nil && (HasExplicitPricingPrice(*card) || len(FilterValidTokenIntervals(card.Intervals)) > 0)
}

// PriceCardNeedsBase reports whether resolution needs token base prices.
func PriceCardNeedsBase(card *ModelPricingEntry) bool {
	return card == nil || (card.BillingMode != BillingModePerRequest && card.BillingMode != BillingModeImage && card.BillingMode != BillingModeVideo)
}

// ResolvePriceCards selects price sources and inherits modifier-only cards using the supplied data.
// @project-doc docs/domains/routing_and_billing.md#group_model_pricing
func ResolvePriceCards(configPricing *ModelPricingEntry, base *ModelPricing, baseSource string, longContextEnabled bool) *ResolvedPricing {
	var resolved *ResolvedPricing
	if PriceCardOverrides(configPricing) {
		resolved = ResolveConfiguredPricing(configPricing, base, PricingSourceConfig)
	} else {
		resolved = &ResolvedPricing{
			Mode:                   BillingModeToken,
			BasePricing:            base,
			Source:                 baseSource,
			SupportsCacheBreakdown: base != nil && base.SupportsCacheBreakdown,
			SupportsServiceTier:    base != nil && base.SupportsServiceTier,
		}
		ApplyPricingModifiers(resolved, configPricing)
	}
	resolved.LongContextPricingEnabled = longContextEnabled
	return resolved
}
