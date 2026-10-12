package billing

import (
	"maps"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// WithPriceCatalog reuses calculation options with a separate catalog snapshot.
func (s *Calculator) WithPriceCatalog(catalog PriceCatalog) *Calculator {
	out := *s
	out.catalog = catalog
	now := s.options.Now()
	out.options.Now = func() time.Time { return now }
	return &out
}

// DefaultModelPrice returns base billing rules for display; an explicit zero price counts as priced.
func (s *Calculator) DefaultModelPrice(model, platform, mode string) pricing.DefaultModelPrice {
	result := pricing.DefaultModelPrice{Model: model, Platform: platform, BillingMode: "token", PriceStatus: "unpriced", Prices: []pricing.DefaultPriceValue{}}
	add := func(key string, value float64, unit string) {
		result.Prices = append(result.Prices, pricing.DefaultPriceValue{Key: key, Value: &value, Unit: unit})
	}
	raw := s.RawModelPricing(model)
	result.BillingMode = pricing.DefaultBillingMode(model, mode, raw)
	if raw != nil {
		result.Source = raw.Source
		result.PriceSources = maps.Clone(raw.PriceSources)
	}
	knownVideo := raw != nil && len(raw.VideoPrices) > 0
	if knownVideo {
		if result.PriceSources == nil {
			result.PriceSources = map[string]string{}
		}
		for _, size := range []string{"480p", "720p", "1080p"} {
			price, ok := pricing.DefaultVideoPrice(raw, size)
			if !ok {
				continue
			}
			add(size, price, "USD/s")
			source := raw.PriceSources["video_prices"]
			if source == "" {
				source = raw.Source
			}
			result.PriceSources[size] = source
		}
		result.PriceStatus = "priced"
		return result
	}
	knownImage := raw != nil && len(raw.ImagePrices) > 0
	imageModel := mode == "image" || pricing.HasExplicitImageGenerationPricing(raw) || pricing.LooksLikeImageModel(model)
	// Chat image metadata keeps token billing. Per-image rates and image models use media units.
	hasImagePrice := knownImage || pricing.HasImageUnitPrice(raw) && (imageModel || raw.TokenPricingAbsent)
	if hasImagePrice {
		if result.PriceSources == nil {
			result.PriceSources = map[string]string{}
		}
		imageSource := result.PriceSources["image"]
		if imageSource == "" {
			imageSource = result.Source
		}
		if knownImage {
			if source := raw.PriceSources["image_prices"]; source != "" {
				imageSource = source
			}
		}
		for _, size := range []string{"1K", "2K", "4K"} {
			price, ok := pricing.DefaultImagePrice(raw, size)
			if !ok {
				continue
			}
			add(size, price, "USD/image")
			if imageSource != "" {
				result.PriceSources[size] = imageSource
			}
		}
		result.PriceStatus = "priced"
	}
	base, err := pricing.ResolveModelPricing(model, raw)
	if err != nil || base == nil {
		return result
	}
	result.PriceStatus = "priced"
	base = s.applyCatalogTimePricing(base, s.options.Now())
	result.Prices = append(result.Prices, defaultTokenPrices(base, raw, "", "", false)...)
	hasFast := false
	if _, ok := pricing.FastModeDisplayPricing(base); ok {
		result.Prices = append(result.Prices, defaultTokenPrices(base, raw, "fast_", "priority", false)...)
		hasFast = true
	}
	hasFlex := base.FlexMultiplier != nil
	if hasFlex {
		result.Prices = append(result.Prices, defaultTokenPrices(base, raw, "flex_", "flex", false)...)
	}
	if base.MaxReasoningEffortMultiplier != nil {
		add("max_reasoning", *base.MaxReasoningEffortMultiplier, "multiplier")
	}
	// Long-context rates include Fast/Flex multipliers. Show the tier when either long-context multiplier exceeds one.
	if base.LongContextInputThreshold > 0 && (base.LongContextInputMultiplier > 1 || base.LongContextOutputMultiplier > 1) {
		result.LongContextThreshold = base.LongContextInputThreshold
		result.LongContextThresholdInclusive = base.LongContextThresholdInclusive
		result.Prices = append(result.Prices, defaultTokenPrices(base, raw, "long_", "", true)...)
		if hasFast {
			result.Prices = append(result.Prices, defaultTokenPrices(base, raw, "long_fast_", "priority", true)...)
		}
		if hasFlex {
			result.Prices = append(result.Prices, defaultTokenPrices(base, raw, "long_flex_", "flex", true)...)
		}
	}

	if len(base.ContextPrices) > 0 {
		start := 0
		for i := 0; i <= len(base.ContextPrices); i++ {
			var end *int
			if i < len(base.ContextPrices) {
				threshold := base.ContextPrices[i].Threshold
				end = &threshold
			}
			intervalPricing := base
			if i > 0 {
				intervalPricing = base.ContextPrices[i-1].Pricing
			}
			prices := defaultTokenPrices(intervalPricing, raw, "", "", false)
			if _, ok := pricing.FastModeDisplayPricing(intervalPricing); ok {
				prices = append(prices, defaultTokenPrices(intervalPricing, raw, "fast_", "priority", false)...)
			}
			if intervalPricing.SupportsServiceTier {
				prices = append(prices, defaultTokenPrices(intervalPricing, raw, "flex_", "flex", false)...)
			}
			result.ContextIntervals = append(result.ContextIntervals, pricing.DefaultPriceInterval{MinTokens: start, MaxTokens: end, Prices: prices})
			if end != nil {
				start = *end
			}
		}
	}
	return result
}

// defaultTokenPrices computes display rates using token billing rules, including cache and image token price defaults.
func defaultTokenPrices(base *pricing.ModelPricing, raw *pricing.CatalogModelPricing, prefix, tier string, applyLongCtx bool) []pricing.DefaultPriceValue {
	prices := make([]pricing.DefaultPriceValue, 0, 7)
	add := func(key string, value float64, unit string) {
		prices = append(prices, pricing.DefaultPriceValue{Key: key, Value: &value, Unit: unit})
	}
	// Quote usage must reach the catalog threshold; exclusive thresholds require one extra input token.
	gate := 0
	if applyLongCtx && base.LongContextInputThreshold > 0 {
		gate = base.LongContextInputThreshold
		if !base.LongContextThresholdInclusive {
			gate++
		}
	}
	quote := func(tokens UsageTokens) *CostBreakdown {
		tokens.InputTokens += gate
		return pricing.ComputeTokenBreakdown(base, tokens, 1, tier, applyLongCtx)
	}
	perMTok := func(cost float64, tokens int) float64 {
		return cost / float64(tokens) * 1e6
	}
	addOptional := func(key string, value float64, applicable bool) {
		if applicable {
			add(prefix+key, value*1e6, "USD/MTok")
		} else {
			prices = append(prices, pricing.DefaultPriceValue{Key: prefix + key, Unit: "USD/MTok"})
		}
	}
	add(prefix+"input", perMTok(quote(UsageTokens{InputTokens: 1}).InputCost, 1+gate), "USD/MTok")
	add(prefix+"output", quote(UsageTokens{OutputTokens: 1}).OutputCost*1e6, "USD/MTok")
	readPresent := base.CacheReadPricePerToken > 0 || raw != nil && raw.CacheReadPricePresent
	writePresent := base.CacheCreationPricePerToken > 0 || base.SupportsCacheBreakdown || raw != nil && raw.CacheCreationPricePresent
	addOptional("cache_read", quote(UsageTokens{CacheReadTokens: 1}).CacheReadCost, readPresent)
	addOptional("cache_write", quote(UsageTokens{CacheCreationTokens: 1, CacheCreation5mTokens: 1}).CacheCreationCost, writePresent)
	addOptional("cache_write_1h", quote(UsageTokens{CacheCreationTokens: 1, CacheCreation1hTokens: 1}).CacheCreationCost, base.SupportsCacheBreakdown)
	addOptional("image_input", quote(UsageTokens{InputTokens: 1, ImageInputTokens: 1}).ImageInputCost, base.ImageInputPricePerToken > 0 || raw != nil && (raw.SupportsVision || raw.ImageInputPricePresent))
	addOptional("image_output", quote(UsageTokens{OutputTokens: 1, ImageOutputTokens: 1}).ImageOutputCost, base.ImageOutputPricePerToken > 0 || raw != nil && raw.ImageOutputPricePresent)
	return prices
}
