package pricing

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// OpenAIFastTierPriority and OpenAIFastTierUltrafast use OpenAI protocol tier names.
const (
	OpenAIFastTierPriority  = openai.ServiceTierPriority
	OpenAIFastTierUltrafast = openai.ServiceTierUltrafast
)

// ErrModelPricingUnavailable indicates that no pricing source supplies a price for the requested model.
var ErrModelPricingUnavailable = errors.New("pricing not found")

// ResolvedTokenPriceRange holds pricing for a positive context range; nil pricing means unpriced.
type ResolvedTokenPriceRange struct {
	minTokens int
	maxTokens *int
	pricing   *ModelPricing
}

func NormalizeBillingServiceTier(serviceTier string) string {
	return strings.ToLower(strings.TrimSpace(serviceTier))
}

func UsePriorityServiceTierPricing(serviceTier string, pricing *ModelPricing) bool {
	if pricing == nil {
		return false
	}
	tier := NormalizeBillingServiceTier(serviceTier)
	if tier != "priority" && tier != "fast" {
		return false
	}
	if pricing.FastModeMultiplier != nil || pricing.FastMultiplier != nil {
		return false
	}
	return pricing.PriorityInputPresent || pricing.PriorityOutputPresent || pricing.PriorityCacheReadPresent || pricing.PriorityCacheWritePresent || pricing.InputPricePerTokenPriority > 0 || pricing.OutputPricePerTokenPriority > 0 ||
		pricing.CacheCreationPricePerTokenPriority > 0 || pricing.CacheReadPricePerTokenPriority > 0
}

// NormalizedFastModeMultiplier returns the configured Fast multiplier, clamping negative values to zero.
func NormalizedFastModeMultiplier(pricing *ModelPricing) (float64, bool) {
	if pricing == nil {
		return 1, false
	}
	configured := pricing.FastModeMultiplier
	if configured == nil {
		configured = pricing.FastMultiplier
	}
	if configured == nil {
		return 1, false
	}
	if *configured < 0 {
		return 0, true
	}
	return *configured, true
}

// ConfiguredServiceTierMultiplier returns the configured tier multiplier, or 1 when absent.
func ConfiguredServiceTierMultiplier(serviceTier string, pricing *ModelPricing) float64 {
	if pricing != nil {
		switch NormalizeBillingServiceTier(serviceTier) {
		case "priority", "fast":
			if multiplier, configured := NormalizedFastModeMultiplier(pricing); configured {
				return multiplier
			}
		case "flex":
			if pricing.FlexMultiplier != nil {
				return *pricing.FlexMultiplier
			}
		}
	}
	return 1
}

// ApplyConfigFastModeMultiplier copies the card's Fast multiplier into resolved pricing metadata.
func ApplyConfigFastModeMultiplier(pricing *ModelPricing, configPricing *ModelPricingEntry) {
	if pricing == nil || configPricing == nil {
		return
	}
	multiplierPtr := configPricing.FastMultiplier
	if multiplierPtr == nil {
		multiplierPtr = configPricing.FastModeMultiplier
	}
	if multiplierPtr == nil {
		return
	}
	multiplier := *multiplierPtr
	if multiplier < 0 {
		multiplier = 0
	}
	pricing.FastModeMultiplier = &multiplier
	pricing.FastMultiplier = &multiplier
}

func ApplyConfigFlexMultiplier(pricing *ModelPricing, configPricing *ModelPricingEntry) {
	if pricing == nil || configPricing == nil || configPricing.FlexMultiplier == nil {
		return
	}
	multiplier := *configPricing.FlexMultiplier
	if multiplier < 0 {
		multiplier = 0
	}
	pricing.FlexMultiplier = &multiplier
}

// ApplyCostBreakdownMultiplier scales every token cost bucket and both totals.
func ApplyCostBreakdownMultiplier(cost *CostBreakdown, multiplier float64) {
	if cost == nil || multiplier == 1 {
		return
	}
	cost.InputCost *= multiplier
	cost.ImageInputCost *= multiplier
	cost.OutputCost *= multiplier
	cost.ImageOutputCost *= multiplier
	cost.CacheCreationCost *= multiplier
	cost.CacheReadCost *= multiplier
	cost.TotalCost *= multiplier
	cost.ActualCost *= multiplier
}

// MaxReasoningEffortBillingMultiplier returns the configured multiplier for max effort, or 1.
func MaxReasoningEffortBillingMultiplier(model, effort string, pricing *ModelPricing) float64 {
	if protocol.NormalizeClaudeOutputEffort(effort) == nil || !strings.EqualFold(strings.TrimSpace(effort), "max") {
		return 1
	}
	if pricing != nil && pricing.MaxReasoningEffortMultiplier != nil && *pricing.MaxReasoningEffortMultiplier > 0 {
		return *pricing.MaxReasoningEffortMultiplier
	}
	return 1
}

// ConfigTierOverridePrice scales the configured standard price by the catalog's tier-to-standard ratio.
// It returns zero when either catalog price is nonpositive.
func ConfigTierOverridePrice(baseStandard, baseTier, configStandard float64) float64 {
	if baseStandard > 0 && baseTier > 0 {
		return configStandard * (baseTier / baseStandard)
	}
	return 0
}

// ApplyConfigTokenPriceOverrides applies text and image token overrides while retaining catalog tier ratios.
func ApplyConfigTokenPriceOverrides(pricing *ModelPricing, configPricing *ModelPricingEntry) {
	if pricing == nil || configPricing == nil {
		return
	}
	// Apply overrides to each context tier, retaining its independently priced buckets where omitted.
	if len(pricing.ContextPrices) > 0 {
		tiers := make([]ContextModelPrice, len(pricing.ContextPrices))
		for i, tier := range pricing.ContextPrices {
			value := *tier.Pricing
			ApplyConfigTokenPriceOverrides(&value, configPricing)
			tiers[i] = ContextModelPrice{Threshold: tier.Threshold, Pricing: &value}
		}
		pricing.ContextPrices = tiers
	}
	if configPricing.InputPrice != nil {
		priority := ConfigTierOverridePrice(pricing.InputPricePerToken, pricing.InputPricePerTokenPriority, *configPricing.InputPrice)
		pricing.InputPricePerToken = *configPricing.InputPrice
		pricing.InputPricePerTokenPriority = priority
	}
	if configPricing.OutputPrice != nil {
		priority := ConfigTierOverridePrice(pricing.OutputPricePerToken, pricing.OutputPricePerTokenPriority, *configPricing.OutputPrice)
		pricing.OutputPricePerToken = *configPricing.OutputPrice
		pricing.OutputPricePerTokenPriority = priority
	}
	if configPricing.CacheWritePrice != nil {
		basePriority := pricing.CacheCreationPricePerTokenPriority
		priority := ConfigTierOverridePrice(pricing.CacheCreationPricePerToken, basePriority, *configPricing.CacheWritePrice)
		pricing.CacheCreationPricePerToken = *configPricing.CacheWritePrice
		pricing.CacheCreationPricePerTokenPriority = priority
		pricing.CacheCreationPriceExplicit = true
		pricing.CacheCreation5mPrice = *configPricing.CacheWritePrice
		if configPricing.CacheWrite1hPrice == nil {
			// When no separate 1h override is supplied, cache_write_price covers both TTLs.
			pricing.CacheCreation1hPrice = *configPricing.CacheWritePrice
		}
	}
	if configPricing.CacheWrite1hPrice != nil {
		pricing.CacheCreation1hPrice = *configPricing.CacheWrite1hPrice
		pricing.SupportsCacheBreakdown = true
	}
	if configPricing.CacheReadPrice != nil {
		priority := ConfigTierOverridePrice(pricing.CacheReadPricePerToken, pricing.CacheReadPricePerTokenPriority, *configPricing.CacheReadPrice)
		pricing.CacheReadPricePerToken = *configPricing.CacheReadPrice
		pricing.CacheReadPricePerTokenPriority = priority
	}
	applyConfigImagePriceOverrides(pricing, configPricing)
}

// CalculateTokenCost charges token usage using the matching context interval.
func CalculateTokenCost(resolved *ResolvedPricing, input CostInput) (*CostBreakdown, error) {
	totalContext := input.Tokens.InputTokens + input.Tokens.CacheCreationTokens + input.Tokens.CacheReadTokens

	pricing := GetIntervalPricing(resolved, totalContext)
	if pricing == nil {
		return nil, fmt.Errorf("no pricing available for model: %s: %w", input.Model, ErrModelPricingUnavailable)
	}

	// Explicit intervals already price context ranges; built-in long-context pricing applies only without them.
	applyLongCtx := len(resolved.Intervals) == 0 && resolved.LongContextPricingEnabled

	breakdown := ComputeTokenBreakdown(pricing, input.Tokens, input.RateMultiplier, input.ServiceTier, applyLongCtx)
	if resolved.Source == PricingSourceCatalog && pricing.TimePricing != nil {
		ApplyCostBreakdownMultiplier(breakdown, pricing.TimePricing.MultiplierAt(input.ModelPricingAt, input.ModelTimeLocation))
	}
	ApplyCostBreakdownMultiplier(breakdown, ResolvedTimeMultiplier(resolved, input.PricingAt, input.TimePricingLocation))
	ApplyCostBreakdownMultiplier(breakdown, MaxReasoningEffortBillingMultiplier(input.Model, input.ReasoningEffort, pricing))
	return breakdown, nil
}

// ComputeTokenBreakdown calculates text, image, and cache token costs with tier and rate multipliers.
// applyLongCtx enables built-in context pricing; callers using explicit intervals disable it.
func ComputeTokenBreakdown(
	pricing *ModelPricing, tokens UsageTokens,
	rateMultiplier float64, serviceTier string,
	applyLongCtx bool,
) *CostBreakdown {
	if applyLongCtx && len(pricing.ContextPrices) > 0 {
		input := tokens.InputTokens + tokens.CacheReadTokens + tokens.CacheCreationTokens
		selected := pricing
		for _, tier := range pricing.ContextPrices {
			if input > tier.Threshold {
				selected = tier.Pricing
			}
		}
		if selected != pricing {
			value := *selected
			value.FastModeMultiplier = pricing.FastModeMultiplier
			value.FastMultiplier = pricing.FastMultiplier
			value.FlexMultiplier = pricing.FlexMultiplier
			value.MaxReasoningEffortMultiplier = pricing.MaxReasoningEffortMultiplier
			cost := ComputeTokenBreakdown(&value, tokens, rateMultiplier, serviceTier, false)
			cost.LongContextBillingApplied = cost.ActualCost > ComputeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, false).ActualCost
			return cost
		}
	}
	// Clamp invalid negative rates to zero rather than charging at the default rate.
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}

	inputPrice := pricing.InputPricePerToken
	outputPrice := pricing.OutputPricePerToken
	cacheReadPrice := pricing.CacheReadPricePerToken
	cacheCreationPrice := pricing.CacheCreationPricePerToken
	cacheCreationMultiplier := 1.0
	tierMultiplier := 1.0

	tier := NormalizeBillingServiceTier(serviceTier)
	if tier == "priority" || tier == "fast" {
		if fastMultiplier, configured := NormalizedFastModeMultiplier(pricing); configured {
			// An explicit Fast multiplier scales standard prices, so catalog priority prices must not also apply.
			tierMultiplier = fastMultiplier
		} else if UsePriorityServiceTierPricing(serviceTier, pricing) {
			if pricing.InputPricePerTokenPriority > 0 || pricing.PriorityInputPresent {
				inputPrice = pricing.InputPricePerTokenPriority
			}
			if pricing.OutputPricePerTokenPriority > 0 || pricing.PriorityOutputPresent {
				outputPrice = pricing.OutputPricePerTokenPriority
			}
			if pricing.CacheReadPricePerTokenPriority > 0 || pricing.PriorityCacheReadPresent {
				cacheReadPrice = pricing.CacheReadPricePerTokenPriority
			}
			if pricing.CacheCreationPricePerTokenPriority > 0 || pricing.PriorityCacheWritePresent {
				cacheCreationPrice = pricing.CacheCreationPricePerTokenPriority
			}
		} else {
			tierMultiplier = ConfiguredServiceTierMultiplier(serviceTier, pricing)
		}
	} else {
		tierMultiplier = ConfiguredServiceTierMultiplier(serviceTier, pricing)
	}

	longContextPricingEligible := applyLongCtx && ShouldApplySessionLongContextPricing(tokens, pricing)
	var baselineCost *CostBreakdown
	if longContextPricingEligible {
		baselineCost = ComputeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, false)
		// An omitted multiplier is 1 so a partial override does not make the other bucket free.
		longContextInputMultiplier := LongContextMultiplierOrOne(pricing.LongContextInputMultiplier)
		inputPrice *= longContextInputMultiplier
		outputPrice *= LongContextMultiplierOrOne(pricing.LongContextOutputMultiplier)
		// Cache reads reuse input tokens and receive the input long-context multiplier.
		// Omitting that multiplier undercharges cache-heavy requests (see #2293).
		cacheReadPrice *= longContextInputMultiplier
		// Cache writes also receive the input multiplier. ComputeCacheCreationCost reads
		// the standard, 5m, and 1h prices directly from pricing, so pass the multiplier
		// separately to apply it to all three prices.
		cacheCreationMultiplier = longContextInputMultiplier
	}

	bd := &CostBreakdown{}
	// Image input tokens may have a separate rate, as in multimodal embeddings or image editing.
	// Record text and image charges separately for reconciliation.
	// When ImageInputTokens is zero, all input tokens use the text rate.
	if tokens.ImageInputTokens > 0 {
		imageInputTokens := tokens.ImageInputTokens
		textInputTokens := tokens.InputTokens - imageInputTokens
		if textInputTokens < 0 {
			textInputTokens = 0
			imageInputTokens = tokens.InputTokens
		}
		imageInputPrice := pricing.ImageInputPricePerToken
		if imageInputPrice == 0 {
			imageInputPrice = inputPrice
		}
		bd.InputCost = float64(textInputTokens) * inputPrice
		bd.ImageInputCost = float64(imageInputTokens) * imageInputPrice
	} else {
		bd.InputCost = float64(tokens.InputTokens) * inputPrice
	}

	// Exclude image output tokens from the text charge.
	textOutputTokens := max(tokens.OutputTokens-tokens.ImageOutputTokens, 0)
	bd.OutputCost = float64(textOutputTokens) * outputPrice

	// Image output tokens use their own rate, with a text-rate default when unspecified.
	if tokens.ImageOutputTokens > 0 {
		imgPrice := pricing.ImageOutputPricePerToken
		if imgPrice == 0 && !pricing.ImageOutputPriceExplicit {
			imgPrice = outputPrice
		}
		bd.ImageOutputCost = float64(tokens.ImageOutputTokens) * imgPrice
	}

	// An explicit priority cache-write price scales TTL prices regardless of the source label.
	if UsePriorityServiceTierPricing(serviceTier, pricing) && pricing.SupportsCacheBreakdown && pricing.CacheCreationPricePerToken > 0 && pricing.PriorityCacheWritePresent {
		cacheCreationMultiplier *= cacheCreationPrice / pricing.CacheCreationPricePerToken
	}
	// Charge cache writes after selecting tier and context multipliers.
	bd.CacheCreationCost = ComputeCacheCreationCost(pricing, tokens, cacheCreationPrice, cacheCreationMultiplier)

	bd.CacheReadCost = float64(tokens.CacheReadTokens) * cacheReadPrice

	if tierMultiplier != 1.0 {
		bd.InputCost *= tierMultiplier
		bd.ImageInputCost *= tierMultiplier
		bd.OutputCost *= tierMultiplier
		bd.ImageOutputCost *= tierMultiplier
		bd.CacheCreationCost *= tierMultiplier
		bd.CacheReadCost *= tierMultiplier
	}

	bd.TotalCost = bd.InputCost + bd.ImageInputCost + bd.OutputCost + bd.ImageOutputCost +
		bd.CacheCreationCost + bd.CacheReadCost
	bd.ActualCost = bd.TotalCost * rateMultiplier
	bd.LongContextBillingApplied = baselineCost != nil && bd.ActualCost > baselineCost.ActualCost

	return bd
}

// ComputeCacheCreationCost charges cache writes using TTL details when supported, or the supplied aggregate price.
// multiplier scales all cache-write prices; pass 1 when no scaling applies.
func ComputeCacheCreationCost(pricing *ModelPricing, tokens UsageTokens, price, multiplier float64) float64 {
	if pricing.SupportsCacheBreakdown && (pricing.CacheCreation5mPrice > 0 || pricing.CacheCreation1hPrice > 0) {
		cacheCreation5mTokens, cacheCreation1hTokens := NormalizeCacheCreationBreakdown(tokens)
		if cacheCreation5mTokens == 0 && cacheCreation1hTokens == 0 && tokens.CacheCreationTokens > 0 {
			// Missing ephemeral details charge all reported cache writes at the 5m price.
			return float64(tokens.CacheCreationTokens) * pricing.CacheCreation5mPrice * multiplier
		}
		return float64(cacheCreation5mTokens)*pricing.CacheCreation5mPrice*multiplier +
			float64(cacheCreation1hTokens)*pricing.CacheCreation1hPrice*multiplier
	}
	return float64(tokens.CacheCreationTokens) * price * multiplier
}

// NormalizeCacheCreationBreakdown caps inconsistent TTL details at a positive aggregate,
// retaining the reported proportions as closely as integer token counts allow.
func NormalizeCacheCreationBreakdown(tokens UsageTokens) (int, int) {
	cacheCreation5mTokens := tokens.CacheCreation5mTokens
	cacheCreation1hTokens := tokens.CacheCreation1hTokens
	aggregate := tokens.CacheCreationTokens
	if cacheCreation5mTokens < 0 {
		cacheCreation5mTokens = 0
	}
	if cacheCreation1hTokens < 0 {
		cacheCreation1hTokens = 0
	}
	if aggregate <= 0 || (cacheCreation5mTokens <= aggregate && cacheCreation1hTokens <= aggregate-cacheCreation5mTokens) {
		return cacheCreation5mTokens, cacheCreation1hTokens
	}

	detailTotal := float64(cacheCreation5mTokens) + float64(cacheCreation1hTokens)
	normalized5mTokens := math.Round(float64(aggregate) * float64(cacheCreation5mTokens) / detailTotal)
	if normalized5mTokens >= float64(aggregate) {
		cacheCreation5mTokens = aggregate
	} else {
		cacheCreation5mTokens = int(normalized5mTokens)
	}
	return cacheCreation5mTokens, aggregate - cacheCreation5mTokens
}

// CalculatePerRequestCost charges the resolved unit price for requests, images, or video units.
func CalculatePerRequestCost(resolved *ResolvedPricing, input CostInput) (*CostBreakdown, error) {
	units := input.UsageUnits
	if units <= 0 {
		count := input.RequestCount
		if count <= 0 {
			count = 1
		}
		units = float64(count)
	}

	totalContext := input.Tokens.InputTokens + input.Tokens.CacheCreationTokens + input.Tokens.CacheReadTokens
	unitPrice, found := ResolveRequestUnitPrice(resolved, input.SizeTier, &totalContext)
	if !found {
		return nil, fmt.Errorf("%w for request price: %s", ErrModelPricingUnavailable, input.Model)
	}

	totalCost := unitPrice * units
	actualCost := totalCost * input.RateMultiplier

	return &CostBreakdown{
		TotalCost:  totalCost,
		ActualCost: actualCost,
	}, nil
}

// LongContextMultiplierOrOne treats nonpositive long-context multipliers as 1.
func LongContextMultiplierOrOne(multiplier float64) float64 {
	if multiplier <= 0 {
		return 1
	}
	return multiplier
}

func ShouldApplySessionLongContextPricing(tokens UsageTokens, pricing *ModelPricing) bool {
	if pricing == nil || pricing.LongContextInputThreshold <= 0 {
		return false
	}
	if pricing.LongContextInputMultiplier <= 1 && pricing.LongContextOutputMultiplier <= 1 {
		return false
	}
	totalInputTokens := tokens.InputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
	if pricing.LongContextThresholdInclusive {
		return totalInputTokens >= pricing.LongContextInputThreshold
	}
	return totalInputTokens > pricing.LongContextInputThreshold
}

// DisplayPricingFromResolved converts resolved prices into marketplace display values.
func DisplayPricingFromResolved(model string, rateMultiplier float64, resolved *ResolvedPricing) (ModelDisplayPricing, bool) {
	if resolved == nil {
		return ModelDisplayPricing{}, false
	}

	switch resolved.Mode {
	case BillingModeToken:
		pricing := ResolvedDisplayTokenPricing(resolved)
		if pricing != nil && !resolved.LongContextPricingEnabled {
			pricing = WithoutLongContextDisplayPricing(pricing)
		}
		if pricing != nil && (HasAnyDisplayTokenPricing(pricing) || resolved.HasEffectiveOverridePricing()) {
			return withTokenDisplayModifiers(BuildTokenDisplayPricing(pricing, rateMultiplier), resolved), true
		}
		intervals := ResolvedDisplayPricingIntervals(resolved, rateMultiplier)
		if len(intervals) > 0 {
			return withTokenDisplayModifiers(BuildTokenIntervalDisplayPricing(intervals), resolved), true
		}
		return ModelDisplayPricing{}, false
	case BillingModeImage, BillingModePerRequest:
		if resolved.Source != PricingSourceConfig {
			return ModelDisplayPricing{}, false
		}
		if resolved.Mode == BillingModePerRequest && !LooksLikeImageModel(model) {
			return ModelDisplayPricing{}, false
		}
		result := ModelDisplayPricing{PricingMode: "image", PriceStatus: "priced"}
		prices := []*float64{&result.ImagePrice1K, &result.ImagePrice2K, &result.ImagePrice4K}
		for i, size := range []string{"1K", "2K", "4K"} {
			value, found := ConfiguredImageUnitPrice(resolved, size)
			if !found {
				continue
			}
			*prices[i] = value * rateMultiplier
			result.ImagePriceSizes = append(result.ImagePriceSizes, size)
		}
		return result, len(result.ImagePriceSizes) > 0
	default:
		return ModelDisplayPricing{}, false
	}
}

// withTokenDisplayModifiers adds the effort multiplier and configured time periods used by settlement.
// It omits unit effort multipliers and empty or invalid time rules.
func withTokenDisplayModifiers(display ModelDisplayPricing, resolved *ResolvedPricing) ModelDisplayPricing {
	if resolved.BasePricing != nil {
		multiplier := resolved.BasePricing.MaxReasoningEffortMultiplier
		if multiplier != nil && *multiplier > 0 && *multiplier != 1 {
			value := *multiplier
			display.MaxReasoningEffortMultiplier = &value
		}
	}
	if resolved.ConfigPricing != nil {
		config := resolved.ConfigPricing.TimePricing
		if config != nil && len(config.Periods) > 0 && ValidateTimePricingConfig(config) == nil {
			display.TimePricing = resolved.ConfigPricing.Clone().TimePricing
		}
	}
	return display
}

// WithoutLongContextDisplayPricing returns a copy with base prices and no built-in context tiers.
func WithoutLongContextDisplayPricing(pricing *ModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}
	cloned := *pricing
	cloned.LongContextInputThreshold = 0
	cloned.ContextPrices = nil
	cloned.LongContextInputMultiplier = 0
	cloned.LongContextOutputMultiplier = 0
	return &cloned
}

// ResolvedTokenPriceRanges fills gaps around explicit intervals with default prices in a separate slice.
func ResolvedTokenPriceRanges(resolved *ResolvedPricing) []ResolvedTokenPriceRange {
	if resolved == nil || len(resolved.Intervals) == 0 {
		return nil
	}
	intervals := append([]PricingInterval(nil), resolved.Intervals...)
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].MinTokens < intervals[j].MinTokens })
	ranges := make([]ResolvedTokenPriceRange, 0, len(intervals)*2+1)
	cursor := 0
	for i := range intervals {
		interval := &intervals[i]
		if interval.MinTokens > cursor {
			maxTokens := interval.MinTokens
			ranges = append(ranges, ResolvedTokenPriceRange{cursor, &maxTokens, WithoutLongContextDisplayPricing(resolved.tokenPricingForInterval(nil))})
		}
		ranges = append(ranges, ResolvedTokenPriceRange{interval.MinTokens, interval.MaxTokens, resolved.tokenPricingForInterval(interval)})
		if interval.MaxTokens == nil {
			return ranges
		}
		cursor = *interval.MaxTokens
	}
	return append(ranges, ResolvedTokenPriceRange{cursor, nil, WithoutLongContextDisplayPricing(resolved.tokenPricingForInterval(nil))})
}

func ResolvedDisplayTokenPricing(resolved *ResolvedPricing) *ModelPricing {
	if resolved == nil {
		return nil
	}
	if len(resolved.Intervals) == 0 {
		return resolved.tokenPricingForInterval(nil)
	}
	ranges := ResolvedTokenPriceRanges(resolved)
	pricing := ranges[0].pricing
	// Missing or unequal prices cannot be displayed as a single price covering all contexts.
	for _, priceRange := range ranges {
		if priceRange.pricing == nil || !SameDisplayTokenPricing(pricing, priceRange.pricing) {
			return nil
		}
	}
	return pricing
}

// ResolvedDisplayPricingIntervals returns priced ranges, omitting unpriced ranges.
func ResolvedDisplayPricingIntervals(resolved *ResolvedPricing, rateMultiplier float64) []ModelDisplayPricingInterval {
	var intervals []ModelDisplayPricingInterval
	for _, priceRange := range ResolvedTokenPriceRanges(resolved) {
		if priceRange.pricing != nil {
			intervals = append(intervals, ModelPricingDisplayInterval(priceRange.minTokens, priceRange.maxTokens, priceRange.pricing, rateMultiplier))
		}
	}
	return intervals
}

func PricingIntervalHasEffectiveTokenPricing(interval PricingInterval) bool {
	return interval.InputPrice != nil ||
		interval.OutputPrice != nil ||
		interval.CacheWritePrice != nil ||
		interval.CacheWrite1hPrice != nil ||
		interval.CacheReadPrice != nil
}

func SameDisplayTokenPricing(a *ModelPricing, b *ModelPricing) bool {
	if a == nil || b == nil {
		return a == b
	}
	// Compare both standard and Fast prices so merging cannot hide service-tier differences.
	return ModelPricingDisplayInterval(0, nil, a, 1) == ModelPricingDisplayInterval(0, nil, b, 1)
}

func BuildTokenDisplayPricing(pricing *ModelPricing, rateMultiplier float64) ModelDisplayPricing {
	if intervals := LongContextDisplayPricingIntervals(pricing, rateMultiplier); len(intervals) > 0 {
		return BuildTokenIntervalDisplayPricing(intervals)
	}

	cacheWritePrice, cacheWrite1hPrice := CacheCreationDisplayPrices(pricing)
	displayPricing := ModelDisplayPricing{
		PricingMode:               "token",
		PriceStatus:               "priced",
		InputPricePerToken:        pricing.InputPricePerToken * rateMultiplier,
		ImageInputPricePerToken:   pricing.ImageInputPricePerToken * rateMultiplier,
		OutputPricePerToken:       pricing.OutputPricePerToken * rateMultiplier,
		CacheWritePricePerToken:   cacheWritePrice * rateMultiplier,
		CacheWrite1hPricePerToken: cacheWrite1hPrice * rateMultiplier,
		CacheReadPricePerToken:    pricing.CacheReadPricePerToken * rateMultiplier,
		ImageOutputPricePerToken:  pricing.ImageOutputPricePerToken * rateMultiplier,
	}
	if fastPricing, ok := FastModeDisplayPricing(pricing); ok {
		displayPricing.FastInputPricePerToken = fastPricing.InputPricePerToken * rateMultiplier
		displayPricing.FastImageInputPricePerToken = fastPricing.ImageInputPricePerToken * rateMultiplier
		displayPricing.FastOutputPricePerToken = fastPricing.OutputPricePerToken * rateMultiplier
		fastCacheWritePrice, fastCacheWrite1hPrice := CacheCreationDisplayPrices(fastPricing)
		displayPricing.FastCacheWritePricePerToken = fastCacheWritePrice * rateMultiplier
		displayPricing.FastCacheWrite1hPricePerToken = fastCacheWrite1hPrice * rateMultiplier
		displayPricing.FastCacheReadPricePerToken = fastPricing.CacheReadPricePerToken * rateMultiplier
		displayPricing.FastImageOutputPricePerToken = fastPricing.ImageOutputPricePerToken * rateMultiplier
	}
	return displayPricing
}

// CacheCreationDisplayPrices returns the displayed 5m and 1h cache-write prices.
// Without TTL breakdown support, it returns the aggregate price and a zero 1h price.
func CacheCreationDisplayPrices(pricing *ModelPricing) (float64, float64) {
	if pricing == nil {
		return 0, 0
	}
	short := pricing.CacheCreationPricePerToken
	if pricing.SupportsCacheBreakdown && pricing.CacheCreation5mPrice > 0 {
		short = pricing.CacheCreation5mPrice
	}
	if !pricing.SupportsCacheBreakdown {
		return short, 0
	}
	return short, pricing.CacheCreation1hPrice
}

// LongContextDisplayPricingIntervals converts built-in context tiers or multipliers into display intervals.
func LongContextDisplayPricingIntervals(pricing *ModelPricing, rateMultiplier float64) []ModelDisplayPricingInterval {
	if pricing != nil && len(pricing.ContextPrices) > 0 {
		var intervals []ModelDisplayPricingInterval
		start := 0
		current := pricing
		for _, tier := range pricing.ContextPrices {
			end := tier.Threshold
			intervals = append(intervals, ModelPricingDisplayInterval(start, &end, current, rateMultiplier))
			next := *tier.Pricing
			next.FastModeMultiplier = pricing.FastModeMultiplier
			next.FastMultiplier = pricing.FastMultiplier
			next.FlexMultiplier = pricing.FlexMultiplier
			next.MaxReasoningEffortMultiplier = pricing.MaxReasoningEffortMultiplier
			start, current = end, &next
		}
		return append(intervals, ModelPricingDisplayInterval(start, nil, current, rateMultiplier))
	}
	if !HasLongContextDisplayPricing(pricing) {
		return nil
	}

	maxTokens := pricing.LongContextInputThreshold
	baseInterval := ModelPricingDisplayInterval(0, &maxTokens, pricing, rateMultiplier)

	longContextPricing := ApplyLongContextDisplayMultipliers(pricing)
	longContextInterval := ModelPricingDisplayInterval(pricing.LongContextInputThreshold, nil, longContextPricing, rateMultiplier)

	return []ModelDisplayPricingInterval{baseInterval, longContextInterval}
}

// ApplyLongContextDisplayMultipliers returns a pricing copy scaled by settlement's context rules.
// Standard, priority, and TTL cache prices all use the input multiplier.
func ApplyLongContextDisplayMultipliers(pricing *ModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}
	adjusted := *pricing
	// As in settlement, an omitted input or output multiplier is 1.
	inputMultiplier := LongContextMultiplierOrOne(pricing.LongContextInputMultiplier)
	outputMultiplier := LongContextMultiplierOrOne(pricing.LongContextOutputMultiplier)
	adjusted.InputPricePerToken *= inputMultiplier
	adjusted.InputPricePerTokenPriority *= inputMultiplier
	adjusted.OutputPricePerToken *= outputMultiplier
	adjusted.OutputPricePerTokenPriority *= outputMultiplier
	adjusted.CacheCreationPricePerToken *= inputMultiplier
	adjusted.CacheCreationPricePerTokenPriority *= inputMultiplier
	adjusted.CacheCreation5mPrice *= inputMultiplier
	adjusted.CacheCreation1hPrice *= inputMultiplier
	adjusted.CacheReadPricePerToken *= inputMultiplier
	adjusted.CacheReadPricePerTokenPriority *= inputMultiplier
	return &adjusted
}

func HasLongContextDisplayPricing(pricing *ModelPricing) bool {
	return HasAnyDisplayTokenPricing(pricing) &&
		pricing.LongContextInputThreshold > 0 &&
		(pricing.LongContextInputMultiplier > 1 || pricing.LongContextOutputMultiplier > 1)
}

func ModelPricingDisplayInterval(minTokens int, maxTokens *int, pricing *ModelPricing, rateMultiplier float64) ModelDisplayPricingInterval {
	cacheWritePrice, cacheWrite1hPrice := CacheCreationDisplayPrices(pricing)
	interval := ModelDisplayPricingInterval{
		MinTokens:                 minTokens,
		MaxTokens:                 maxTokens,
		InputPricePerToken:        pricing.InputPricePerToken * rateMultiplier,
		ImageInputPricePerToken:   pricing.ImageInputPricePerToken * rateMultiplier,
		OutputPricePerToken:       pricing.OutputPricePerToken * rateMultiplier,
		CacheWritePricePerToken:   cacheWritePrice * rateMultiplier,
		CacheWrite1hPricePerToken: cacheWrite1hPrice * rateMultiplier,
		CacheReadPricePerToken:    pricing.CacheReadPricePerToken * rateMultiplier,
		ImageOutputPricePerToken:  pricing.ImageOutputPricePerToken * rateMultiplier,
	}
	if fastPricing, ok := FastModeDisplayPricing(pricing); ok {
		interval.FastInputPricePerToken = fastPricing.InputPricePerToken * rateMultiplier
		interval.FastImageInputPricePerToken = fastPricing.ImageInputPricePerToken * rateMultiplier
		interval.FastOutputPricePerToken = fastPricing.OutputPricePerToken * rateMultiplier
		fastCacheWritePrice, fastCacheWrite1hPrice := CacheCreationDisplayPrices(fastPricing)
		interval.FastCacheWritePricePerToken = fastCacheWritePrice * rateMultiplier
		interval.FastCacheWrite1hPricePerToken = fastCacheWrite1hPrice * rateMultiplier
		interval.FastCacheReadPricePerToken = fastPricing.CacheReadPricePerToken * rateMultiplier
		interval.FastImageOutputPricePerToken = fastPricing.ImageOutputPricePerToken * rateMultiplier
	}
	return interval
}

func FastModeDisplayPricing(pricing *ModelPricing) (*ModelPricing, bool) {
	if !HasFastModeDisplayPricing(pricing) {
		return nil, false
	}
	if multiplier, configured := NormalizedFastModeMultiplier(pricing); configured {
		fastPricing := MultiplyModelPricing(pricing, multiplier)
		fastPricing.FastModeMultiplier = nil
		return fastPricing, true
	}

	fastPricing := *pricing
	if UsePriorityServiceTierPricing(OpenAIFastTierPriority, pricing) {
		if pricing.InputPricePerTokenPriority > 0 || pricing.PriorityInputPresent {
			fastPricing.InputPricePerToken = pricing.InputPricePerTokenPriority
		}
		if pricing.OutputPricePerTokenPriority > 0 || pricing.PriorityOutputPresent {
			fastPricing.OutputPricePerToken = pricing.OutputPricePerTokenPriority
		}
		if pricing.CacheCreationPricePerTokenPriority > 0 || pricing.PriorityCacheWritePresent {
			fastPricing.CacheCreationPricePerToken = pricing.CacheCreationPricePerTokenPriority
			if pricing.SupportsCacheBreakdown && pricing.CacheCreationPricePerToken > 0 {
				ratio := pricing.CacheCreationPricePerTokenPriority / pricing.CacheCreationPricePerToken
				fastPricing.CacheCreation5mPrice *= ratio
				fastPricing.CacheCreation1hPrice *= ratio
			}
		}
		if pricing.CacheReadPricePerTokenPriority > 0 || pricing.PriorityCacheReadPresent {
			fastPricing.CacheReadPricePerToken = pricing.CacheReadPricePerTokenPriority
		}
		return &fastPricing, true
	}

	multiplier := ConfiguredServiceTierMultiplier(OpenAIFastTierPriority, pricing)
	fastPricing.InputPricePerToken *= multiplier
	fastPricing.ImageInputPricePerToken *= multiplier
	fastPricing.OutputPricePerToken *= multiplier
	fastPricing.CacheCreationPricePerToken *= multiplier
	fastPricing.CacheReadPricePerToken *= multiplier
	fastPricing.ImageOutputPricePerToken *= multiplier
	return &fastPricing, true
}

// ResolvedHasFastModeDisplayPricing checks default prices and intervals, including interval-only custom models.
func ResolvedHasFastModeDisplayPricing(resolved *ResolvedPricing) bool {
	if resolved == nil || resolved.Mode != BillingModeToken {
		return false
	}
	if HasFastModeDisplayPricing(resolved.tokenPricingForInterval(nil)) {
		return true
	}
	for i := range resolved.Intervals {
		pricing := resolved.tokenPricingForInterval(&resolved.Intervals[i])
		if HasFastModeDisplayPricing(pricing) {
			return true
		}
	}
	return false
}

func HasFastModeDisplayPricing(pricing *ModelPricing) bool {
	return HasAnyDisplayTokenPricing(pricing) &&
		(pricing.FastModeMultiplier != nil ||
			pricing.FastMultiplier != nil || pricing.PriorityInputPresent || pricing.PriorityOutputPresent || pricing.PriorityCacheReadPresent || pricing.PriorityCacheWritePresent ||
			pricing.InputPricePerTokenPriority > 0 ||
			pricing.OutputPricePerTokenPriority > 0 ||
			pricing.CacheCreationPricePerTokenPriority > 0 ||
			pricing.CacheReadPricePerTokenPriority > 0)
}

// BuildTokenIntervalDisplayPricing marks prices for display by context interval.
func BuildTokenIntervalDisplayPricing(intervals []ModelDisplayPricingInterval) ModelDisplayPricing {
	return ModelDisplayPricing{
		PricingMode:      "token",
		PriceStatus:      "priced",
		ContextIntervals: intervals,
	}
}

func BuildImageDisplayPricing(price1K, price2K, price4K float64) ModelDisplayPricing {
	return ModelDisplayPricing{
		PricingMode:     "image",
		PriceStatus:     "priced",
		ImagePriceSizes: []string{"1K", "2K", "4K"},
		ImagePrice1K:    price1K,
		ImagePrice2K:    price2K,
		ImagePrice4K:    price4K,
	}
}

func UnknownDisplayPricing() ModelDisplayPricing {
	return ModelDisplayPricing{
		PricingMode: "unknown",
		PriceStatus: "unpriced",
	}
}

// CalculateWebSearchCost charges Codex alpha/search calls at the supplied per-call price.
// callCount is the number of search calls.
// groupPrice is the resolved per-call price; nil means unavailable and zero means free.
// rateMultiplier scales the user's charge.
func CalculateWebSearchCost(callCount int, groupPrice *float64, rateMultiplier float64) *CostBreakdown {
	if callCount <= 0 {
		return &CostBreakdown{}
	}
	if groupPrice == nil {
		return &CostBreakdown{}
	}
	unitPrice := *groupPrice
	if groupPrice != nil && *groupPrice >= 0 {
		unitPrice = *groupPrice
	}
	totalCost := unitPrice * float64(callCount)

	// Clamp invalid negative rates to zero.
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  totalCost * rateMultiplier,
		BillingMode: string(BillingModePerRequest),
	}
}

// CalculateSearchCost charges calls using a caller-supplied price per thousand calls; explicit zero is free.
func CalculateSearchCost(numCalls int, groupPricePer1k *float64, rateMultiplier float64) *CostBreakdown {
	if numCalls <= 0 {
		return &CostBreakdown{}
	}
	if groupPricePer1k == nil {
		return &CostBreakdown{}
	}
	pricePer1k := *groupPricePer1k
	if groupPricePer1k != nil {
		if *groupPricePer1k < 0 {
			return &CostBreakdown{}
		}
		pricePer1k = *groupPricePer1k
	}
	if pricePer1k == 0 {
		return &CostBreakdown{}
	}
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	unit := pricePer1k / 1000.0
	total := unit * float64(numCalls)
	return &CostBreakdown{
		TotalCost:   total,
		ActualCost:  total * rateMultiplier,
		BillingMode: string(BillingModePerRequest),
	}
}

// CalculateAudioCost charges Realtime minutes, TTS millions of characters, or STT hours.
// The caller resolves prices and supplies usage in those units; explicit zero is free.
func CalculateAudioCost(mode string, durationOrUnits float64, groupConfig *AudioPriceConfig, rateMultiplier float64) *CostBreakdown {
	if durationOrUnits <= 0 {
		return &CostBreakdown{}
	}
	var unitPrice float64
	switch strings.ToLower(mode) {
	case "realtime":
		if groupConfig != nil && groupConfig.RealtimePerMin != nil {
			unitPrice = *groupConfig.RealtimePerMin
		}
	case "tts":
		if groupConfig != nil && groupConfig.TTSPerMChars != nil {
			unitPrice = *groupConfig.TTSPerMChars
		}
	case "stt":
		if groupConfig != nil && groupConfig.STTPerHour != nil {
			unitPrice = *groupConfig.STTPerHour
		}
	default:
		return &CostBreakdown{}
	}
	if unitPrice <= 0 {
		return &CostBreakdown{}
	}
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	total := unitPrice * durationOrUnits
	return &CostBreakdown{
		TotalCost:   total,
		ActualCost:  total * rateMultiplier,
		BillingMode: string(BillingModePerRequest),
	}
}

// HasExplicitImageGenerationPricing requires per-image prices and the image_generation mode.
// Chat models may carry output_cost_per_image metadata while retaining token billing.
func HasExplicitImageGenerationPricing(pricing *CatalogModelPricing) bool {
	return HasImageUnitPrice(pricing) &&
		strings.EqualFold(strings.TrimSpace(pricing.Mode), "image_generation")
}

func HasAnyDisplayTokenPricing(pricing *ModelPricing) bool {
	if pricing == nil {
		return false
	}
	return pricing.CatalogSource != "" && pricing.CatalogSource != "unpriced" || pricing.InputPricePerToken > 0 ||
		pricing.ImageInputPricePerToken > 0 ||
		pricing.OutputPricePerToken > 0 ||
		pricing.CacheCreationPricePerToken > 0 ||
		pricing.CacheCreation1hPrice > 0 ||
		pricing.CacheReadPricePerToken > 0 ||
		pricing.ImageOutputPricePerToken > 0
}

func LooksLikeImageModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}

	return strings.Contains(model, "-image") ||
		strings.Contains(model, "image-") ||
		strings.Contains(model, "/image") ||
		strings.HasPrefix(model, "imagen-") ||
		strings.Contains(model, "gpt-image") ||
		strings.Contains(model, "dall-e")
}

// CalculateCost dispatches by billing mode, clamps negative rates to zero, and records the mode used.
func CalculateCost(resolved *ResolvedPricing, input CostInput) (*CostBreakdown, error) {
	// Treat negative rates from cached or migrated data as zero at billing time.
	if input.RateMultiplier < 0 {
		input.RateMultiplier = 0
	}

	var breakdown *CostBreakdown
	var err error
	switch resolved.Mode {
	case BillingModePerRequest, BillingModeImage, BillingModeVideo:
		breakdown, err = CalculatePerRequestCost(resolved, input)
	default: // BillingModeToken
		breakdown, err = CalculateTokenCost(resolved, input)
	}
	if err == nil && breakdown != nil {
		breakdown.BillingMode = string(resolved.Mode)
		if breakdown.BillingMode == "" {
			breakdown.BillingMode = string(BillingModeToken)
		}
	}
	return breakdown, err
}
