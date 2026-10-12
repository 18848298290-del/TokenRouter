package pricing

import (
	"slices"
	"strings"
)

// Pricing source labels identify configured, catalog, and unavailable prices.
const (
	PricingSourceConfig   = "pricing_config"
	PricingSourceCatalog  = "catalog"
	PricingSourceUnpriced = "unpriced"
)

// ResolvedPricing contains the effective billing mode, prices, and configuration.
type ResolvedPricing struct {
	// Mode selects the units used for billing.
	Mode BillingMode

	// Base token prices supplied to resolution.
	BasePricing *ModelPricing

	// Token intervals override corresponding BasePricing fields.
	Intervals []PricingInterval

	// Labeled or context-based prices for request and media billing.
	RequestTiers []PricingInterval

	// Default unit price when no tier matches.
	DefaultPerRequestPrice float64
	// DefaultPerRequestPricePresent distinguishes an explicit free default from an omitted price.
	DefaultPerRequestPricePresent bool `json:"-"`

	// Source identifies where the base prices were resolved.
	Source string // "pricing_config", "catalog", or "unpriced"

	// Whether cache writes support separate TTL prices.
	SupportsCacheBreakdown bool

	// Whether service-tier pricing is supported.
	SupportsServiceTier bool

	// The effective card, also used for image prices and modifiers during interval resolution.
	ConfigPricing *ModelPricingEntry `json:"-"`

	LongContextPricingEnabled bool `json:"-"`
	// A placeholder may carry modifiers without base prices; explicit zero prices remain available.
	BasePricingUnavailable bool `json:"-"`
}

// ApplyPricingModifiers overrides modifiers on a copy of existing token prices.
func ApplyPricingModifiers(resolved *ResolvedPricing, config *ModelPricingEntry) {
	if resolved == nil || resolved.Mode != BillingModeToken || config == nil || resolved.BasePricing == nil {
		return
	}
	if config.FastMultiplier == nil && config.FastModeMultiplier == nil && config.FlexMultiplier == nil &&
		config.MaxReasoningEffortMultiplier == nil && config.TimePricing == nil {
		return
	}
	pricing := *resolved.BasePricing
	resolved.BasePricing = &pricing
	merged := ModelPricingEntry{BillingMode: BillingModeToken}
	if resolved.ConfigPricing != nil {
		merged = resolved.ConfigPricing.Clone()
	}
	// Interval resolution rereads card metadata, so update the copied card along with base pricing.
	if config.FastMultiplier != nil || config.FastModeMultiplier != nil {
		merged.FastMultiplier = config.FastMultiplier
		merged.FastModeMultiplier = config.FastModeMultiplier
	}
	if config.FlexMultiplier != nil {
		merged.FlexMultiplier = config.FlexMultiplier
	}
	if config.MaxReasoningEffortMultiplier != nil {
		merged.MaxReasoningEffortMultiplier = config.MaxReasoningEffortMultiplier
		pricing.MaxReasoningEffortMultiplier = config.MaxReasoningEffortMultiplier
	}
	if config.TimePricing != nil && len(config.TimePricing.Periods) > 0 {
		merged.TimePricing = config.Clone().TimePricing
	}
	resolved.ConfigPricing = &merged
	ApplyResolvedFastModeMultiplier(resolved, &merged)
}

func (r *ResolvedPricing) IsUnpriced() bool {
	if r == nil {
		return false
	}
	if r.Source == PricingSourceUnpriced {
		return true
	}
	// With a configured token card, modifier-only data needs a base price; other paths may resolve media prices separately.
	if r.Mode != BillingModeToken || r.ConfigPricing == nil || r.hasBaseTokenPricing() {
		return false
	}
	return !slices.ContainsFunc(r.Intervals, PricingIntervalHasEffectiveTokenPricing)
}

// hasBaseTokenPricing distinguishes available prices, including free prices, from modifier placeholders.
func (r *ResolvedPricing) hasBaseTokenPricing() bool {
	return r != nil && r.BasePricing != nil && !r.BasePricingUnavailable
}

// tokenPricingForInterval serves settlement and display; modifier-only intervals without a base remain unpriced.
func (r *ResolvedPricing) tokenPricingForInterval(interval *PricingInterval) *ModelPricing {
	if r == nil {
		return nil
	}
	if interval == nil {
		if r.hasBaseTokenPricing() {
			return r.BasePricing
		}
		return nil
	}
	if !r.hasBaseTokenPricing() && !PricingIntervalHasEffectiveTokenPricing(*interval) {
		return nil
	}
	pricing := IntervalToModelPricingWithBase(interval, r.SupportsCacheBreakdown, r.ConfigPricing, r.BasePricing)
	pricing.SupportsServiceTier = r.SupportsServiceTier
	return pricing
}

func (r *ResolvedPricing) HasEffectivePricing() bool {
	return r != nil && !r.IsUnpriced() && r.Source == PricingSourceConfig && r.ConfigPricing != nil && r.ConfigPricing.HasEffectivePricing()
}

// HasConfiguredPricing reports whether resolution used a card, including modifier-only cards with catalog base prices.
// Media billing uses this result because Source alone does not indicate whether a card was configured.
func (r *ResolvedPricing) HasConfiguredPricing() bool {
	return r != nil && r.ConfigPricing != nil
}

// HasEffectiveOverridePricing reports whether effective configuration pricing exists, including explicit zero.
func (r *ResolvedPricing) HasEffectiveOverridePricing() bool {
	return r != nil && !r.IsUnpriced() && r.Source == PricingSourceConfig &&
		r.ConfigPricing != nil && r.ConfigPricing.HasEffectivePricing()
}

// ApplyTokenOverrides applies token-card defaults and intervals to resolved pricing.
func ApplyTokenOverrides(chPricing *ModelPricingEntry, resolved *ResolvedPricing) {
	// Keep intervals with token prices or token multipliers.
	validIntervals := FilterValidTokenIntervals(chPricing.Intervals)
	// An explicit 1h cache price enables TTL detail billing.
	if chPricing.CacheWrite1hPrice != nil {
		resolved.SupportsCacheBreakdown = true
	}
	for _, iv := range validIntervals {
		if iv.CacheWrite1hPrice != nil {
			resolved.SupportsCacheBreakdown = true
			break
		}
	}

	// Apply card defaults to an independent base before storing intervals. Missing interval buckets,
	// interval multipliers, and requests outside intervals all use that base.
	// Only default prices establish a base; interval prices apply when that interval matches.
	baseConfig := *chPricing
	baseConfig.Intervals = nil
	resolved.BasePricingUnavailable = !resolved.hasBaseTokenPricing() && !HasExplicitPricingPrice(baseConfig)
	resolved.Intervals = validIntervals
	if resolved.BasePricing == nil {
		resolved.BasePricing = &ModelPricing{}
	} else {
		// Keep shared catalog prices immutable.
		cloned := *resolved.BasePricing
		resolved.BasePricing = &cloned
	}

	ApplyConfigTokenPriceOverrides(resolved.BasePricing, chPricing)
	if resolved.SupportsCacheBreakdown {
		resolved.BasePricing.SupportsCacheBreakdown = true
	}
	if chPricing.MaxReasoningEffortMultiplier != nil {
		resolved.BasePricing.MaxReasoningEffortMultiplier = chPricing.MaxReasoningEffortMultiplier
	}
}

// applyConfigImagePriceOverrides applies image-card rules to a base price or context tier.
// Omitted or zero image output is free; omitted or zero image input uses that tier's text input price.
func applyConfigImagePriceOverrides(pricing *ModelPricing, chPricing *ModelPricingEntry) {
	pricing.ImageOutputPricePerToken = 0
	if chPricing.ImageOutputPrice != nil {
		pricing.ImageOutputPricePerToken = *chPricing.ImageOutputPrice
	}
	pricing.ImageOutputPriceExplicit = true
	if chPricing.ImageInputPrice != nil {
		pricing.ImageInputPricePerToken = *chPricing.ImageInputPrice
	} else {
		pricing.ImageInputPricePerToken = 0
	}
}

// ApplyRequestTierOverrides applies request and media unit-price overrides.
func ApplyRequestTierOverrides(chPricing *ModelPricingEntry, resolved *ResolvedPricing) {
	resolved.RequestTiers = FilterValidRequestIntervals(chPricing.Intervals)
	if chPricing.PerRequestPrice != nil {
		resolved.DefaultPerRequestPrice = *chPricing.PerRequestPrice
		resolved.DefaultPerRequestPricePresent = true
	}
}

// ApplyResolvedPriceMultiplier scales prices after card overrides.
// Callers validate that a price multiplier accompanies an explicit price.
func ApplyResolvedPriceMultiplier(resolved *ResolvedPricing, chPricing *ModelPricingEntry) {
	multiplier, configured := NormalizedPriceMultiplier(chPricing)
	if resolved == nil || !configured {
		return
	}

	resolved.BasePricing = MultiplyModelPricing(resolved.BasePricing, multiplier)
	resolved.Intervals = MultiplyPricingIntervals(resolved.Intervals, multiplier)
	resolved.RequestTiers = MultiplyPricingIntervals(resolved.RequestTiers, multiplier)
	resolved.DefaultPerRequestPrice *= multiplier

	// Interval conversion reads card-level image prices, so keep a correspondingly scaled card copy.
	scaledConfigPricing := chPricing.Clone()
	scaledConfigPricing.PriceMultiplier = nil
	MultiplyPricingFields(&scaledConfigPricing, multiplier)
	resolved.ConfigPricing = &scaledConfigPricing
}

// ApplyResolvedFastModeMultiplier adds Fast and Flex multipliers after standard prices are resolved and scaled.
func ApplyResolvedFastModeMultiplier(resolved *ResolvedPricing, chPricing *ModelPricingEntry) {
	if resolved == nil || resolved.Mode != BillingModeToken {
		return
	}
	ApplyConfigFastModeMultiplier(resolved.BasePricing, chPricing)
	ApplyConfigFlexMultiplier(resolved.BasePricing, chPricing)
}

// NormalizedPriceMultiplier returns the multiplier and whether it was set, clamping negative values to zero.
func NormalizedPriceMultiplier(pricing *ModelPricingEntry) (float64, bool) {
	if pricing == nil || pricing.PriceMultiplier == nil {
		return 1, false
	}
	if *pricing.PriceMultiplier < 0 {
		return 0, true
	}
	return *pricing.PriceMultiplier, true
}

// MultiplyModelPricing copies and scales amounts, retaining context thresholds and multiplier settings.
func MultiplyModelPricing(pricing *ModelPricing, multiplier float64) *ModelPricing {
	if pricing == nil {
		return nil
	}
	scaled := *pricing
	scaled.ContextPrices = make([]ContextModelPrice, len(pricing.ContextPrices))
	for i, tier := range pricing.ContextPrices {
		scaled.ContextPrices[i] = ContextModelPrice{Threshold: tier.Threshold, Pricing: MultiplyModelPricing(tier.Pricing, multiplier)}
	}
	scaled.InputPricePerToken *= multiplier
	scaled.InputPricePerTokenPriority *= multiplier
	scaled.ImageInputPricePerToken *= multiplier
	scaled.OutputPricePerToken *= multiplier
	scaled.OutputPricePerTokenPriority *= multiplier
	scaled.CacheCreationPricePerToken *= multiplier
	scaled.CacheCreationPricePerTokenPriority *= multiplier
	scaled.CacheReadPricePerToken *= multiplier
	scaled.CacheReadPricePerTokenPriority *= multiplier
	scaled.CacheCreation5mPrice *= multiplier
	scaled.CacheCreation1hPrice *= multiplier
	scaled.ImageOutputPricePerToken *= multiplier
	return &scaled
}

// MultiplyPricingIntervals copies the interval slice and scales its price fields.
func MultiplyPricingIntervals(intervals []PricingInterval, multiplier float64) []PricingInterval {
	if intervals == nil {
		return nil
	}
	scaled := make([]PricingInterval, len(intervals))
	for i := range intervals {
		scaled[i] = intervals[i]
		scaled[i].InputPrice = MultiplyPricePointer(intervals[i].InputPrice, multiplier)
		scaled[i].OutputPrice = MultiplyPricePointer(intervals[i].OutputPrice, multiplier)
		scaled[i].CacheWritePrice = MultiplyPricePointer(intervals[i].CacheWritePrice, multiplier)
		scaled[i].CacheWrite1hPrice = MultiplyPricePointer(intervals[i].CacheWrite1hPrice, multiplier)
		scaled[i].CacheReadPrice = MultiplyPricePointer(intervals[i].CacheReadPrice, multiplier)
		scaled[i].PerRequestPrice = MultiplyPricePointer(intervals[i].PerRequestPrice, multiplier)
	}
	return scaled
}

// MultiplyPricingFields replaces a card's explicit prices with scaled copies.
func MultiplyPricingFields(pricing *ModelPricingEntry, multiplier float64) {
	if pricing == nil {
		return
	}
	pricing.InputPrice = MultiplyPricePointer(pricing.InputPrice, multiplier)
	pricing.OutputPrice = MultiplyPricePointer(pricing.OutputPrice, multiplier)
	pricing.CacheWritePrice = MultiplyPricePointer(pricing.CacheWritePrice, multiplier)
	pricing.CacheWrite1hPrice = MultiplyPricePointer(pricing.CacheWrite1hPrice, multiplier)
	pricing.CacheReadPrice = MultiplyPricePointer(pricing.CacheReadPrice, multiplier)
	pricing.ImageInputPrice = MultiplyPricePointer(pricing.ImageInputPrice, multiplier)
	pricing.ImageOutputPrice = MultiplyPricePointer(pricing.ImageOutputPrice, multiplier)
	pricing.PerRequestPrice = MultiplyPricePointer(pricing.PerRequestPrice, multiplier)
	pricing.Intervals = MultiplyPricingIntervals(pricing.Intervals, multiplier)
}

func MultiplyPricePointer(price *float64, multiplier float64) *float64 {
	if price == nil {
		return nil
	}
	scaled := *price * multiplier
	return &scaled
}

// FilterValidTokenIntervals keeps intervals with token prices or token multipliers.
// Empty editor intervals and per_request-only fields left after a mode change
// do not constitute token pricing.
func FilterValidTokenIntervals(intervals []PricingInterval) []PricingInterval {
	var valid []PricingInterval
	for _, iv := range intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil ||
			iv.CacheWritePrice != nil || iv.CacheWrite1hPrice != nil || iv.CacheReadPrice != nil ||
			iv.InputMultiplier != nil || iv.OutputMultiplier != nil ||
			iv.CacheWriteMultiplier != nil || iv.CacheReadMultiplier != nil {
			valid = append(valid, iv)
		}
	}
	return valid
}

// FilterValidRequestIntervals keeps request and media tiers with an explicit per-request price.
func FilterValidRequestIntervals(intervals []PricingInterval) []PricingInterval {
	var valid []PricingInterval
	for _, iv := range intervals {
		if iv.PerRequestPrice != nil {
			valid = append(valid, iv)
		}
	}
	return valid
}

// GetIntervalPricing selects prices for the supplied context token count.
// A matching interval overrides the base; no match uses available base pricing.
func GetIntervalPricing(resolved *ResolvedPricing, totalContextTokens int) *ModelPricing {
	return resolved.tokenPricingForInterval(FindMatchingInterval(resolved.Intervals, totalContextTokens))
}

// IntervalToModelPricingWithBase converts an interval to model prices, inheriting unspecified fields from base.
func IntervalToModelPricingWithBase(iv *PricingInterval, supportsCacheBreakdown bool, chPricing *ModelPricingEntry, base *ModelPricing) *ModelPricing {
	if iv == nil {
		return base
	}
	pricing := &ModelPricing{
		SupportsCacheBreakdown: supportsCacheBreakdown,
	}
	if base != nil {
		cloned := *base
		pricing = &cloned
		pricing.SupportsCacheBreakdown = supportsCacheBreakdown
		// The explicit interval already defines the context range; clear built-in long-context
		// multipliers so display and billing do not apply another threshold rule.
		pricing.LongContextInputThreshold = 0
		pricing.LongContextInputMultiplier = 0
		pricing.LongContextOutputMultiplier = 0
	}
	if iv.InputPrice != nil {
		priority := ConfigTierOverridePrice(pricing.InputPricePerToken, pricing.InputPricePerTokenPriority, *iv.InputPrice)
		pricing.InputPricePerToken = *iv.InputPrice
		pricing.InputPricePerTokenPriority = priority
	} else if iv.InputMultiplier != nil {
		pricing.InputPricePerToken *= *iv.InputMultiplier
		pricing.InputPricePerTokenPriority *= *iv.InputMultiplier
	}
	if iv.OutputPrice != nil {
		priority := ConfigTierOverridePrice(pricing.OutputPricePerToken, pricing.OutputPricePerTokenPriority, *iv.OutputPrice)
		pricing.OutputPricePerToken = *iv.OutputPrice
		pricing.OutputPricePerTokenPriority = priority
	} else if iv.OutputMultiplier != nil {
		pricing.OutputPricePerToken *= *iv.OutputMultiplier
		pricing.OutputPricePerTokenPriority *= *iv.OutputMultiplier
	}
	if iv.CacheWritePrice != nil {
		priority := ConfigTierOverridePrice(pricing.CacheCreationPricePerToken, pricing.CacheCreationPricePerTokenPriority, *iv.CacheWritePrice)
		pricing.CacheCreationPricePerToken = *iv.CacheWritePrice
		pricing.CacheCreationPricePerTokenPriority = priority
		pricing.CacheCreationPriceExplicit = true
		pricing.CacheCreation5mPrice = *iv.CacheWritePrice
		if iv.CacheWrite1hPrice == nil {
			// A cache_write_price override covers both TTLs when no separate 1h override is supplied.
			pricing.CacheCreation1hPrice = *iv.CacheWritePrice
		}
	} else if iv.CacheWriteMultiplier != nil {
		pricing.CacheCreationPricePerToken *= *iv.CacheWriteMultiplier
		pricing.CacheCreationPricePerTokenPriority *= *iv.CacheWriteMultiplier
		pricing.CacheCreation5mPrice *= *iv.CacheWriteMultiplier
		pricing.CacheCreation1hPrice *= *iv.CacheWriteMultiplier
	}
	if iv.CacheWrite1hPrice != nil {
		pricing.CacheCreation1hPrice = *iv.CacheWrite1hPrice
		pricing.SupportsCacheBreakdown = true
	}
	if iv.CacheReadPrice != nil {
		priority := ConfigTierOverridePrice(pricing.CacheReadPricePerToken, pricing.CacheReadPricePerTokenPriority, *iv.CacheReadPrice)
		pricing.CacheReadPricePerToken = *iv.CacheReadPrice
		pricing.CacheReadPricePerTokenPriority = priority
	} else if iv.CacheReadMultiplier != nil {
		pricing.CacheReadPricePerToken *= *iv.CacheReadMultiplier
		pricing.CacheReadPricePerTokenPriority *= *iv.CacheReadMultiplier
	}
	// Image input/output overrides come from the card because intervals have no image-price fields.
	if chPricing != nil {
		applyConfigImagePriceOverrides(pricing, chPricing)
		ApplyConfigFastModeMultiplier(pricing, chPricing)
		ApplyConfigFlexMultiplier(pricing, chPricing)
	}
	return pricing
}

// GetRequestTierPrice returns a labeled unit price, or zero if no tier matches.
func GetRequestTierPrice(resolved *ResolvedPricing, tierLabel string) float64 {
	price, ok := GetRequestTierPriceValue(resolved, tierLabel)
	if !ok {
		return 0
	}
	return price
}

func GetRequestTierPriceValue(resolved *ResolvedPricing, tierLabel string) (float64, bool) {
	tierLabel = strings.TrimSpace(tierLabel)
	if resolved == nil || tierLabel == "" {
		return 0, false
	}
	for _, tier := range resolved.RequestTiers {
		if strings.EqualFold(strings.TrimSpace(tier.TierLabel), tierLabel) && tier.PerRequestPrice != nil {
			return *tier.PerRequestPrice, true
		}
	}
	return 0, false
}

// GetRequestTierPriceByContext returns an unlabeled context-tier price, or zero if no tier matches.
func GetRequestTierPriceByContext(resolved *ResolvedPricing, totalContextTokens int) float64 {
	price, ok := GetRequestTierPriceByContextValue(resolved, totalContextTokens)
	if !ok {
		return 0
	}
	return price
}

// GetRequestTierPriceByContextValue matches context intervals with no label and reports price presence.
func GetRequestTierPriceByContextValue(resolved *ResolvedPricing, totalContextTokens int) (float64, bool) {
	if resolved == nil {
		return 0, false
	}
	for _, iv := range resolved.RequestTiers {
		if strings.TrimSpace(iv.TierLabel) != "" {
			continue
		}
		if totalContextTokens > iv.MinTokens && (iv.MaxTokens == nil || totalContextTokens <= *iv.MaxTokens) && iv.PerRequestPrice != nil {
			return *iv.PerRequestPrice, true
		}
	}
	return 0, false
}

// ResolveRequestUnitPrice tries label, context interval, then default; unknown context requires a fixed price.
func ResolveRequestUnitPrice(resolved *ResolvedPricing, label string, totalContext *int) (float64, bool) {
	if resolved == nil {
		return 0, false
	}
	if value, found := GetRequestTierPriceValue(resolved, label); found {
		return value, true
	}
	hasDefault := resolved.DefaultPerRequestPricePresent ||
		resolved.DefaultPerRequestPrice > 0 ||
		resolved.ConfigPricing != nil && resolved.ConfigPricing.PerRequestPrice != nil
	if totalContext != nil {
		if value, found := GetRequestTierPriceByContextValue(resolved, *totalContext); found {
			return value, true
		}
	} else if resolved.Mode == BillingModePerRequest {
		for _, iv := range resolved.RequestTiers {
			if strings.TrimSpace(iv.TierLabel) != "" || iv.PerRequestPrice == nil {
				continue
			}
			// A fixed quote requires every context price to equal the explicit default, including a free card.
			if !hasDefault || *iv.PerRequestPrice != resolved.DefaultPerRequestPrice {
				return 0, false
			}
		}
	}
	if hasDefault {
		return resolved.DefaultPerRequestPrice, true
	}
	return 0, false
}

// HasExplicitPricingPrice reports supplied price amounts, including zero, independently of tier multipliers.
// A price_multiplier or legacy fast_mode_multiplier alone does not supply a base price.
func HasExplicitPricingPrice(p ModelPricingEntry) bool {
	mode := p.BillingMode
	if mode == "" {
		mode = BillingModeToken
	}
	if mode == BillingModePerRequest || mode == BillingModeImage || mode == BillingModeVideo {
		if p.PerRequestPrice != nil {
			return true
		}
		for _, iv := range p.Intervals {
			if iv.PerRequestPrice != nil {
				return true
			}
		}
		return false
	}
	if p.InputPrice != nil || p.OutputPrice != nil || p.CacheWritePrice != nil || p.CacheWrite1hPrice != nil ||
		p.CacheReadPrice != nil || p.ImageInputPrice != nil || p.ImageOutputPrice != nil {
		return true
	}
	for _, iv := range p.Intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil || iv.CacheWritePrice != nil || iv.CacheWrite1hPrice != nil || iv.CacheReadPrice != nil {
			return true
		}
	}
	return false
}

// NormalizePriceModelName normalizes case and surrounding whitespace while retaining the full model name.
func NormalizePriceModelName(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// ApplyConfigPrice applies card overrides to a copy of pricing; pricing must be non-nil when a card is supplied.
func ApplyConfigPrice(pricing *ModelPricing, configPricing *ModelPricingEntry) *ModelPricing {
	if configPricing == nil {
		return pricing
	}
	// Copy before overriding shared catalog prices.
	cloned := *pricing
	pricing = &cloned
	ApplyConfigTokenPriceOverrides(pricing, configPricing)
	multiplier, configured := NormalizedPriceMultiplier(configPricing)
	if configured {
		pricing = MultiplyModelPricing(pricing, multiplier)
	}
	ApplyConfigFastModeMultiplier(pricing, configPricing)
	ApplyConfigFlexMultiplier(pricing, configPricing)
	if configPricing.MaxReasoningEffortMultiplier != nil {
		pricing.MaxReasoningEffortMultiplier = configPricing.MaxReasoningEffortMultiplier
	}
	return pricing
}
