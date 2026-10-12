package pricing

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	BillingModeToken      BillingMode = "token"       // Token pricing by context interval.
	BillingModePerRequest BillingMode = "per_request" // Per-request pricing with optional context tiers.
	BillingModeImage      BillingMode = "image"       // Per-image pricing.
	BillingModeVideo      BillingMode = "video"       // Video pricing per output second.
)

// BillingMode selects the units used to charge usage.
type BillingMode string

// ProviderStatsPricingRule matches groups and providers to model prices for provider statistics.
// Each rule has its own model cards.
// Rules are ordered by SortOrder; the first matching rule applies.
type ProviderStatsPricingRule struct {
	ID              int64
	PricingConfigID int64
	Name            string
	GroupIDs        []int64
	ProviderIDs     []int64
	SortOrder       int
	Pricing         []ModelPricingEntry // Model cards used by this rule.
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ModelPricingEntry defines a model price card.
type ModelPricingEntry struct {
	ID                 int64       `json:"id,omitempty"`
	PricingConfigID    int64       `json:"pricing_config_id,omitempty"`
	Models             []string    `json:"models"`
	BillingMode        BillingMode `json:"billing_mode"`
	PriceMultiplier    *float64    `json:"price_multiplier"`     // Price multiplier; nil leaves prices unchanged.
	FastModeMultiplier *float64    `json:"fast_mode_multiplier"` // Fast multiplier; nil inherits the model's Fast pricing.
	// FastMultiplier is the Fast/priority multiplier and takes precedence over FastModeMultiplier.
	FastMultiplier *float64 `json:"fast_multiplier,omitempty"`
	// FlexMultiplier overrides the model's Flex multiplier when supplied.
	FlexMultiplier *float64 `json:"flex_multiplier,omitempty"`
	// MaxReasoningEffortMultiplier applies to max effort; nil inherits the model multiplier.
	MaxReasoningEffortMultiplier *float64 `json:"max_reasoning_effort_multiplier,omitempty"`
	InputPrice                   *float64 `json:"input_price"`
	OutputPrice                  *float64 `json:"output_price"`
	CacheWritePrice              *float64 `json:"cache_write_price"`
	// CacheWrite1hPrice overrides the 1h cache-write price; a CacheWritePrice override also covers 1h when this is nil.
	CacheWrite1hPrice *float64           `json:"cache_write_1h_price"`
	CacheReadPrice    *float64           `json:"cache_read_price"`
	ImageInputPrice   *float64           `json:"image_input_price"`
	ImageOutputPrice  *float64           `json:"image_output_price"`
	PerRequestPrice   *float64           `json:"per_request_price"`
	Intervals         []PricingInterval  `json:"intervals"`
	TimePricing       *TimePricingConfig `json:"time_pricing,omitempty"`
	CreatedAt         time.Time          `json:"created_at,omitempty"`
	UpdatedAt         time.Time          `json:"updated_at,omitempty"`
}

// TimePricingConfig defines time-dependent price multipliers.
type TimePricingConfig struct {
	Timezone     string              `json:"timezone"`
	WeekdaysOnly bool                `json:"weekdays_only,omitempty"`
	Periods      []TimePricingPeriod `json:"periods"`
}

// TimePricingPeriod is a second-resolution [start, end) interval that also accepts HH:mm values.
type TimePricingPeriod struct {
	StartTime  string  `json:"start_time"`
	EndTime    string  `json:"end_time"`
	Multiplier float64 `json:"multiplier"`
}

// PricingInterval defines a token context range or a labeled request, image, or video tier.
type PricingInterval struct {
	ID              int64    `json:"id,omitempty"`
	PricingID       int64    `json:"pricing_id,omitempty"`
	MinTokens       int      `json:"min_tokens"`
	MaxTokens       *int     `json:"max_tokens"`
	TierLabel       string   `json:"tier_label"`
	InputPrice      *float64 `json:"input_price"`
	OutputPrice     *float64 `json:"output_price"`
	CacheWritePrice *float64 `json:"cache_write_price"`
	// CacheWrite1hPrice is the optional 1h cache-write price for this interval.
	CacheWrite1hPrice    *float64  `json:"cache_write_1h_price"`
	CacheReadPrice       *float64  `json:"cache_read_price"`
	InputMultiplier      *float64  `json:"input_multiplier,omitempty"`
	OutputMultiplier     *float64  `json:"output_multiplier,omitempty"`
	CacheWriteMultiplier *float64  `json:"cache_write_multiplier,omitempty"`
	CacheReadMultiplier  *float64  `json:"cache_read_multiplier,omitempty"`
	PerRequestPrice      *float64  `json:"per_request_price"`
	SortOrder            int       `json:"sort_order"`
	CreatedAt            time.Time `json:"created_at,omitempty"`
	UpdatedAt            time.Time `json:"updated_at,omitempty"`
}

// IsValid reports whether the billing mode is supported, including an empty default.
func (m BillingMode) IsValid() bool {
	switch m {
	case BillingModeToken, BillingModePerRequest, BillingModeImage, BillingModeVideo, "":
		return true
	}
	return false
}

// IsValidUsageFilter reports whether the mode is accepted by usage filters.
func (m BillingMode) IsValidUsageFilter() bool {
	switch m {
	case BillingModeToken, BillingModePerRequest, BillingModeImage, BillingModeVideo, "":
		return true
	}
	return false
}

// FindMatchingInterval returns the first interval containing totalTokens.
// Intervals are (min, max], excluding the lower endpoint and including the upper endpoint.
// Zero tokens do not match an interval starting at zero; callers use the default price.
func FindMatchingInterval(intervals []PricingInterval, totalTokens int) *PricingInterval {
	for i := range intervals {
		iv := &intervals[i]
		if totalTokens > iv.MinTokens && (iv.MaxTokens == nil || totalTokens <= *iv.MaxTokens) {
			return iv
		}
	}
	return nil
}

// GetTierByLabel finds a tier by case-insensitive label.
func (p *ModelPricingEntry) GetTierByLabel(label string) *PricingInterval {
	labelLower := strings.ToLower(label)
	for i := range p.Intervals {
		if strings.EqualFold(p.Intervals[i].TierLabel, labelLower) {
			return &p.Intervals[i]
		}
	}
	return nil
}

// HasEffectivePricing reports whether the card supplies prices or modifiers that can inherit base prices.
// A nil price is unspecified; a pointer to zero is an explicit free price.
func (p *ModelPricingEntry) HasEffectivePricing() bool {
	if p == nil {
		return false
	}
	mode := p.BillingMode
	if mode == "" {
		mode = BillingModeToken
	}
	switch mode {
	case BillingModePerRequest, BillingModeImage, BillingModeVideo:
		if p.PerRequestPrice != nil {
			return true
		}
		for i := range p.Intervals {
			if p.Intervals[i].PerRequestPrice != nil {
				return true
			}
		}
		return false
	default:
		if p.InputPrice != nil ||
			p.OutputPrice != nil ||
			p.CacheWritePrice != nil ||
			p.CacheWrite1hPrice != nil ||
			p.CacheReadPrice != nil ||
			p.ImageInputPrice != nil ||
			p.ImageOutputPrice != nil ||
			p.FastMultiplier != nil ||
			p.FlexMultiplier != nil ||
			p.MaxReasoningEffortMultiplier != nil ||
			(p.TimePricing != nil && len(p.TimePricing.Periods) > 0) {
			return true
		}
		for i := range p.Intervals {
			iv := p.Intervals[i]
			if iv.InputPrice != nil ||
				iv.OutputPrice != nil ||
				iv.CacheWritePrice != nil ||
				iv.CacheWrite1hPrice != nil ||
				iv.CacheReadPrice != nil ||
				iv.InputMultiplier != nil ||
				iv.OutputMultiplier != nil ||
				iv.CacheWriteMultiplier != nil ||
				iv.CacheReadMultiplier != nil {
				return true
			}
		}
	}
	return false
}

// Clone copies the card with independent nested slices, configuration, and amount pointers.
func (p ModelPricingEntry) Clone() ModelPricingEntry {
	cp := p
	// Independent amount pointers prevent edits to copied cards from changing the source.
	ClonePricingAmounts(&cp.PriceMultiplier, &cp.FastModeMultiplier, &cp.FastMultiplier, &cp.FlexMultiplier,
		&cp.MaxReasoningEffortMultiplier, &cp.InputPrice, &cp.OutputPrice, &cp.CacheWritePrice,
		&cp.CacheWrite1hPrice, &cp.CacheReadPrice, &cp.ImageInputPrice, &cp.ImageOutputPrice, &cp.PerRequestPrice)
	if p.Models != nil {
		cp.Models = make([]string, len(p.Models))
		copy(cp.Models, p.Models)
	}
	if p.Intervals != nil {
		cp.Intervals = make([]PricingInterval, len(p.Intervals))
		copy(cp.Intervals, p.Intervals)
		for i := range cp.Intervals {
			iv := &cp.Intervals[i]
			ClonePricingAmounts(&iv.InputPrice, &iv.OutputPrice, &iv.CacheWritePrice, &iv.CacheWrite1hPrice,
				&iv.CacheReadPrice, &iv.InputMultiplier, &iv.OutputMultiplier, &iv.CacheWriteMultiplier,
				&iv.CacheReadMultiplier, &iv.PerRequestPrice)
			if iv.MaxTokens != nil {
				maxTokens := *iv.MaxTokens
				iv.MaxTokens = &maxTokens
			}
		}
	}
	if p.TimePricing != nil {
		cp.TimePricing = &TimePricingConfig{
			Timezone:     p.TimePricing.Timezone,
			WeekdaysOnly: p.TimePricing.WeekdaysOnly,
		}
		if p.TimePricing.Periods != nil {
			cp.TimePricing.Periods = append([]TimePricingPeriod(nil), p.TimePricing.Periods...)
		}
	}
	return cp
}

// ClonePricingAmounts copies optional amounts, retaining the distinction between nil and explicit zero.
func ClonePricingAmounts(fields ...**float64) {
	for _, field := range fields {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
}

// ValidateIntervals validates interval fields and token-range overlap.
//
// The billing mode determines how intervals are matched:
//   - BillingModeToken (including empty): context ranges are (min, max],
//     cannot overlap, and an unbounded range (MaxTokens=nil) must sort last.
//   - BillingModePerRequest: nonempty labels take priority, then unlabeled context ranges in configuration order.
//   - BillingModeImage / BillingModeVideo: labels select sizes or resolutions.
//     These three non-token modes validate individual fields and allow overlapping ranges.
//
// MinTokens must be nonnegative; a supplied MaxTokens must be positive and greater than MinTokens.
// Prices must be finite and nonnegative; supplied multipliers must be finite and positive.
func ValidateIntervals(intervals []PricingInterval, mode BillingMode) error {
	if len(intervals) == 0 {
		return nil
	}
	sorted := make([]PricingInterval, len(intervals))
	copy(sorted, intervals)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].MinTokens < sorted[j].MinTokens
	})

	for i := range sorted {
		if err := ValidateSingleInterval(&sorted[i], i); err != nil {
			return err
		}
	}

	// Non-token modes select by label or configuration order, so overlapping context ranges are allowed.
	if mode == BillingModePerRequest || mode == BillingModeImage || mode == BillingModeVideo {
		return nil
	}
	return ValidateIntervalOverlap(sorted)
}

// ValidateSingleInterval checks one interval's endpoints, prices, and multipliers.
func ValidateSingleInterval(iv *PricingInterval, idx int) error {
	if iv.MinTokens < 0 {
		return fmt.Errorf("interval #%d: min_tokens (%d) must be >= 0", idx+1, iv.MinTokens)
	}
	if iv.MaxTokens != nil {
		if *iv.MaxTokens <= 0 {
			return fmt.Errorf("interval #%d: max_tokens (%d) must be > 0", idx+1, *iv.MaxTokens)
		}
		if *iv.MaxTokens <= iv.MinTokens {
			return fmt.Errorf("interval #%d: max_tokens (%d) must be > min_tokens (%d)",
				idx+1, *iv.MaxTokens, iv.MinTokens)
		}
	}
	return ValidateIntervalPrices(iv, idx)
}

// ValidateIntervalPrices requires finite nonnegative prices and finite positive multipliers.
func ValidateIntervalPrices(iv *PricingInterval, idx int) error {
	prices := []struct {
		name string
		val  *float64
	}{
		{"input_price", iv.InputPrice},
		{"output_price", iv.OutputPrice},
		{"cache_write_price", iv.CacheWritePrice},
		{"cache_write_1h_price", iv.CacheWrite1hPrice},
		{"cache_read_price", iv.CacheReadPrice},
		{"per_request_price", iv.PerRequestPrice},
	}
	for _, p := range prices {
		if p.val != nil && (math.IsNaN(*p.val) || math.IsInf(*p.val, 0) || *p.val < 0) {
			return fmt.Errorf("interval #%d: %s must be >= 0", idx+1, p.name)
		}
	}
	multipliers := []struct {
		name string
		val  *float64
	}{
		{"input_multiplier", iv.InputMultiplier},
		{"output_multiplier", iv.OutputMultiplier},
		{"cache_write_multiplier", iv.CacheWriteMultiplier},
		{"cache_read_multiplier", iv.CacheReadMultiplier},
	}
	for _, multiplier := range multipliers {
		if multiplier.val != nil && (math.IsNaN(*multiplier.val) || math.IsInf(*multiplier.val, 0) || *multiplier.val <= 0) {
			return fmt.Errorf("interval #%d: %s must be > 0", idx+1, multiplier.name)
		}
	}
	return nil
}

// ValidateIntervalOverlap requires sorted, nonoverlapping ranges with any unbounded range last.
func ValidateIntervalOverlap(sorted []PricingInterval) error {
	for i, iv := range sorted {
		// An unbounded range overlaps every subsequent range.
		if iv.MaxTokens == nil && i < len(sorted)-1 {
			return fmt.Errorf("interval #%d: unbounded interval (max_tokens=null) must be the last one",
				i+1)
		}
		if i == 0 {
			continue
		}
		prev := sorted[i-1]
		// For (min, max] intervals, equal adjacent endpoints are allowed.
		// A preceding maximum greater than the current minimum means overlap.
		if prev.MaxTokens == nil || *prev.MaxTokens > iv.MinTokens {
			return fmt.Errorf("interval #%d and #%d overlap: prev max=%s > cur min=%d",
				i, i+1, FormatMaxTokensLabel(prev.MaxTokens), iv.MinTokens)
		}
	}
	return nil
}

func FormatMaxTokensLabel(max *int) string {
	if max == nil {
		return "∞"
	}
	return fmt.Sprintf("%d", *max)
}

// MatchPriceCard prefers an exact model match, then the first matching wildcard card.
func MatchPriceCard(cards []ModelPricingEntry, candidate string) *ModelPricingEntry {
	candidate = NormalizePriceModelName(candidate)
	var wildcard *ModelPricingEntry
	for i := range cards {
		entry := &cards[i]
		if !entry.HasEffectivePricing() {
			continue
		}
		for _, pattern := range entry.Models {
			normalized := NormalizePriceModelName(pattern)
			if normalized == candidate {
				cp := entry.Clone()
				return &cp
			}
			if strings.HasSuffix(normalized, "*") && strings.HasPrefix(candidate, strings.TrimSuffix(normalized, "*")) && wildcard == nil {
				cp := entry.Clone()
				wildcard = &cp
			}
		}
	}
	return wildcard
}
