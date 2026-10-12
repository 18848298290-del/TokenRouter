package billing

import (
	"maps"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// WithPriceCatalog 在独立目录快照上复用相同算法、数据规则。
func (s *Calculator) WithPriceCatalog(catalog PriceCatalog) *Calculator {
	out := *s
	out.catalog = catalog
	now := s.options.Now()
	out.options.Now = func() time.Time { return now }
	return &out
}

// DefaultModelPrice 返回模型的基础计费规则，配置的零价按已定价处理。
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
	// 聊天模型附带的图片元数据不切换 token 模式；纯按张价和明确的图片模型使用媒体单位。
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
	// 长上下文单价包含 Fast/Flex 倍率。任一长上下文倍率大于 1 才展示阶梯。
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

// defaultTokenPrices 用一个计费单位计算展示单价，缓存和图片 token 回退由计费算法处理。
func defaultTokenPrices(base *pricing.ModelPricing, raw *pricing.CatalogModelPricing, prefix, tier string, applyLongCtx bool) []pricing.DefaultPriceValue {
	prices := make([]pricing.DefaultPriceValue, 0, 7)
	add := func(key string, value float64, unit string) {
		prices = append(prices, pricing.DefaultPriceValue{Key: key, Value: &value, Unit: unit})
	}
	// 报价用量需达到目录阈值；严格大于阈值的模型额外加一个输入 token。
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
