package dto

import (
	"slices"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

type ModelMarketplaceStats struct {
	TodayTokens int64 `json:"today_tokens"`
	TotalTokens int64 `json:"total_tokens"`
	TotalUsers  int64 `json:"total_users"`
}

type ModelMarketplacePricing struct {
	PricingMode                   string                            `json:"pricing_mode"`
	PriceStatus                   string                            `json:"price_status"`
	InputPricePerToken            float64                           `json:"input_price_per_token,omitempty"`
	ImageInputPricePerToken       float64                           `json:"image_input_price_per_token,omitempty"`
	OutputPricePerToken           float64                           `json:"output_price_per_token,omitempty"`
	CacheWritePricePerToken       float64                           `json:"cache_write_price_per_token,omitempty"`
	CacheWrite1hPricePerToken     float64                           `json:"cache_write_1h_price_per_token,omitempty"`
	CacheReadPricePerToken        float64                           `json:"cache_read_price_per_token,omitempty"`
	ImageOutputPricePerToken      float64                           `json:"image_output_price_per_token,omitempty"`
	FastInputPricePerToken        float64                           `json:"fast_input_price_per_token,omitempty"`
	FastImageInputPricePerToken   float64                           `json:"fast_image_input_price_per_token,omitempty"`
	FastOutputPricePerToken       float64                           `json:"fast_output_price_per_token,omitempty"`
	FastCacheWritePricePerToken   float64                           `json:"fast_cache_write_price_per_token,omitempty"`
	FastCacheWrite1hPricePerToken float64                           `json:"fast_cache_write_1h_price_per_token,omitempty"`
	FastCacheReadPricePerToken    float64                           `json:"fast_cache_read_price_per_token,omitempty"`
	FastImageOutputPricePerToken  float64                           `json:"fast_image_output_price_per_token,omitempty"`
	ContextIntervals              []ModelMarketplacePricingInterval `json:"context_intervals,omitempty"`
	MaxReasoningEffortMultiplier  *float64                          `json:"max_reasoning_effort_multiplier,omitempty"`
	TimePricing                   *ModelMarketplaceTimePricing      `json:"time_pricing,omitempty"`
	ImagePrice1K                  *float64                          `json:"image_price_1k,omitempty"`
	ImagePrice2K                  *float64                          `json:"image_price_2k,omitempty"`
	ImagePrice4K                  *float64                          `json:"image_price_4k,omitempty"`
}

// ModelMarketplacePricingInterval contains marketplace prices for a context-length range.
type ModelMarketplacePricingInterval struct {
	MinTokens                     int     `json:"min_tokens"`
	MaxTokens                     *int    `json:"max_tokens,omitempty"`
	InputPricePerToken            float64 `json:"input_price_per_token,omitempty"`
	ImageInputPricePerToken       float64 `json:"image_input_price_per_token,omitempty"`
	OutputPricePerToken           float64 `json:"output_price_per_token,omitempty"`
	CacheWritePricePerToken       float64 `json:"cache_write_price_per_token,omitempty"`
	CacheWrite1hPricePerToken     float64 `json:"cache_write_1h_price_per_token,omitempty"`
	CacheReadPricePerToken        float64 `json:"cache_read_price_per_token,omitempty"`
	ImageOutputPricePerToken      float64 `json:"image_output_price_per_token,omitempty"`
	FastInputPricePerToken        float64 `json:"fast_input_price_per_token,omitempty"`
	FastImageInputPricePerToken   float64 `json:"fast_image_input_price_per_token,omitempty"`
	FastOutputPricePerToken       float64 `json:"fast_output_price_per_token,omitempty"`
	FastCacheWritePricePerToken   float64 `json:"fast_cache_write_price_per_token,omitempty"`
	FastCacheWrite1hPricePerToken float64 `json:"fast_cache_write_1h_price_per_token,omitempty"`
	FastCacheReadPricePerToken    float64 `json:"fast_cache_read_price_per_token,omitempty"`
	FastImageOutputPricePerToken  float64 `json:"fast_image_output_price_per_token,omitempty"`
}

// ModelMarketplaceTimePricing applies token-price multipliers by request time; outside its periods the multiplier is 1.
type ModelMarketplaceTimePricing struct {
	Timezone     string                              `json:"timezone"`
	WeekdaysOnly bool                                `json:"weekdays_only"`
	Periods      []ModelMarketplaceTimePricingPeriod `json:"periods"`
}

// ModelMarketplaceTimePricingPeriod is a daily [start, end) interval. An end of 00:00 means 24:00 that day.
type ModelMarketplaceTimePricingPeriod struct {
	StartTime  string  `json:"start_time"`
	EndTime    string  `json:"end_time"`
	Multiplier float64 `json:"multiplier"`
}

type ModelMarketplaceModel struct {
	Attributes       *routing.EffectiveModelAttributes `json:"attributes,omitempty"`
	ID               string                            `json:"id"`
	DisplayName      string                            `json:"display_name"`
	Pricing          ModelMarketplacePricing           `json:"pricing"`
	InputModalities  []string                          `json:"input_modalities,omitempty"`
	OutputModalities []string                          `json:"output_modalities,omitempty"`
	Protocols        []protocol.ProtocolID             `json:"protocols,omitempty"`
	NativeProtocols  []protocol.ProtocolID             `json:"native_protocols,omitempty"`
}

type ModelMarketplaceCapacity struct {
	ConcurrencyUsed int `json:"concurrency_used"`
	ConcurrencyMax  int `json:"concurrency_max"`
	SessionsUsed    int `json:"sessions_used"`
	SessionsMax     int `json:"sessions_max"`
	RPMUsed         int `json:"rpm_used"`
	RPMMax          int `json:"rpm_max"`
}

type ModelMarketplaceAvailabilityDay struct {
	Date             string   `json:"date"`
	SuccessCount     int64    `json:"success_count"`
	TotalCount       int64    `json:"total_count"`
	AvailabilityRate *float64 `json:"availability_rate,omitempty"`
}

type ModelMarketplaceAvailability struct {
	WindowDays       int                               `json:"window_days"`
	BucketMinutes    int                               `json:"bucket_minutes"`
	SuccessCount     int64                             `json:"success_count"`
	TotalCount       int64                             `json:"total_count"`
	AvailabilityRate *float64                          `json:"availability_rate,omitempty"`
	LastStatus       string                            `json:"last_status,omitempty"`
	LastCheckedAt    *time.Time                        `json:"last_checked_at,omitempty"`
	Days             []ModelMarketplaceAvailabilityDay `json:"days"`
}

type ModelMarketplaceGroup struct {
	SearchTerms []string          `json:"search_terms,omitempty"`
	Resolution  locale.Resolution `json:"localization_resolution"`
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`

	DisplayBrand               string                        `json:"display_brand"`
	SortOrder                  int                           `json:"sort_order"`
	RateMultiplier             float64                       `json:"rate_multiplier"`
	OfficialPriceRatio         *float64                      `json:"official_price_ratio,omitempty"`
	OfficialPriceRMBEquivalent *float64                      `json:"official_price_rmb_equivalent,omitempty"`
	Capacity                   *ModelMarketplaceCapacity     `json:"capacity,omitempty"`
	Availability               *ModelMarketplaceAvailability `json:"availability,omitempty"`
	ModelCount                 int                           `json:"model_count"`
	Models                     []ModelMarketplaceModel       `json:"models"`
}

func ModelMarketplaceGroupsFromRouting(groups []routing.ModelMarketplaceGroup) []ModelMarketplaceGroup {
	out := make([]ModelMarketplaceGroup, 0, len(groups))
	for _, group := range groups {
		models := make([]ModelMarketplaceModel, 0, len(group.Models))
		for _, model := range group.Models {
			models = append(models, ModelMarketplaceModel{
				ID:               model.ID,
				DisplayName:      model.DisplayName,
				Pricing:          modelMarketplacePricingFromRouting(model.Pricing),
				InputModalities:  model.InputModalities,
				Attributes:       model.Attributes,
				OutputModalities: model.OutputModalities,
				Protocols:        model.Protocols,
				NativeProtocols:  model.NativeProtocols,
			})
		}

		out = append(out, ModelMarketplaceGroup{
			SearchTerms: group.SearchTerms, Resolution: group.Resolution,
			ID:          group.ID,
			Name:        group.Name,
			Description: group.Description,

			DisplayBrand:               group.DisplayBrand,
			SortOrder:                  group.SortOrder,
			RateMultiplier:             group.RateMultiplier,
			OfficialPriceRatio:         group.OfficialPriceRatio,
			OfficialPriceRMBEquivalent: group.OfficialPriceRMBEquivalent,
			Capacity:                   modelMarketplaceCapacityFromRouting(group.Capacity),
			Availability:               modelMarketplaceAvailabilityFromRouting(group.Availability),
			ModelCount:                 group.ModelCount,
			Models:                     models,
		})
	}

	return out
}

// modelMarketplaceAvailabilityFromRouting converts probe availability to the public DTO.
func modelMarketplaceAvailabilityFromRouting(availability *routing.GroupAvailabilitySummary) *ModelMarketplaceAvailability {
	if availability == nil {
		return nil
	}
	days := make([]ModelMarketplaceAvailabilityDay, 0, len(availability.Days))
	for _, day := range availability.Days {
		days = append(days, ModelMarketplaceAvailabilityDay{
			Date:             day.Date,
			SuccessCount:     day.SuccessCount,
			TotalCount:       day.TotalCount,
			AvailabilityRate: day.AvailabilityRate,
		})
	}
	return &ModelMarketplaceAvailability{
		WindowDays:       availability.WindowDays,
		BucketMinutes:    availability.BucketMinutes,
		SuccessCount:     availability.SuccessCount,
		TotalCount:       availability.TotalCount,
		AvailabilityRate: availability.AvailabilityRate,
		LastStatus:       availability.LastStatus,
		LastCheckedAt:    availability.LastCheckedAt,
		Days:             days,
	}
}

// modelMarketplaceCapacityFromRouting converts group capacity to the public DTO.
func modelMarketplaceCapacityFromRouting(capacity *routing.GroupCapacitySummary) *ModelMarketplaceCapacity {
	if capacity == nil {
		return nil
	}
	return &ModelMarketplaceCapacity{
		ConcurrencyUsed: capacity.ConcurrencyUsed,
		ConcurrencyMax:  capacity.ConcurrencyMax,
		SessionsUsed:    capacity.SessionsUsed,
		SessionsMax:     capacity.SessionsMax,
		RPMUsed:         capacity.RPMUsed,
		RPMMax:          capacity.RPMMax,
	}
}

// modelMarketplacePricingFromRouting converts display prices to the HTTP DTO.
func modelMarketplacePricingFromRouting(pricing pricing.ModelDisplayPricing) ModelMarketplacePricing {
	intervals := make([]ModelMarketplacePricingInterval, 0, len(pricing.ContextIntervals))
	for _, interval := range pricing.ContextIntervals {
		intervals = append(intervals, ModelMarketplacePricingInterval{
			MinTokens:                     interval.MinTokens,
			MaxTokens:                     interval.MaxTokens,
			InputPricePerToken:            interval.InputPricePerToken,
			ImageInputPricePerToken:       interval.ImageInputPricePerToken,
			OutputPricePerToken:           interval.OutputPricePerToken,
			CacheWritePricePerToken:       interval.CacheWritePricePerToken,
			CacheWrite1hPricePerToken:     interval.CacheWrite1hPricePerToken,
			CacheReadPricePerToken:        interval.CacheReadPricePerToken,
			ImageOutputPricePerToken:      interval.ImageOutputPricePerToken,
			FastInputPricePerToken:        interval.FastInputPricePerToken,
			FastImageInputPricePerToken:   interval.FastImageInputPricePerToken,
			FastOutputPricePerToken:       interval.FastOutputPricePerToken,
			FastCacheWritePricePerToken:   interval.FastCacheWritePricePerToken,
			FastCacheWrite1hPricePerToken: interval.FastCacheWrite1hPricePerToken,
			FastCacheReadPricePerToken:    interval.FastCacheReadPricePerToken,
			FastImageOutputPricePerToken:  interval.FastImageOutputPricePerToken,
		})
	}

	return ModelMarketplacePricing{
		PricingMode:                   pricing.PricingMode,
		PriceStatus:                   pricing.PriceStatus,
		InputPricePerToken:            pricing.InputPricePerToken,
		ImageInputPricePerToken:       pricing.ImageInputPricePerToken,
		OutputPricePerToken:           pricing.OutputPricePerToken,
		CacheWritePricePerToken:       pricing.CacheWritePricePerToken,
		CacheWrite1hPricePerToken:     pricing.CacheWrite1hPricePerToken,
		CacheReadPricePerToken:        pricing.CacheReadPricePerToken,
		ImageOutputPricePerToken:      pricing.ImageOutputPricePerToken,
		FastInputPricePerToken:        pricing.FastInputPricePerToken,
		FastImageInputPricePerToken:   pricing.FastImageInputPricePerToken,
		FastOutputPricePerToken:       pricing.FastOutputPricePerToken,
		FastCacheWritePricePerToken:   pricing.FastCacheWritePricePerToken,
		FastCacheWrite1hPricePerToken: pricing.FastCacheWrite1hPricePerToken,
		FastCacheReadPricePerToken:    pricing.FastCacheReadPricePerToken,
		FastImageOutputPricePerToken:  pricing.FastImageOutputPricePerToken,
		ContextIntervals:              intervals,
		MaxReasoningEffortMultiplier:  pricing.MaxReasoningEffortMultiplier,
		TimePricing:                   modelMarketplaceTimePricingFromRouting(pricing.TimePricing),
		ImagePrice1K:                  imagePriceValue(pricing, "1K", pricing.ImagePrice1K),
		ImagePrice2K:                  imagePriceValue(pricing, "2K", pricing.ImagePrice2K),
		ImagePrice4K:                  imagePriceValue(pricing, "4K", pricing.ImagePrice4K),
	}
}

// modelMarketplaceTimePricingFromRouting converts time-based pricing rules to the public DTO.
func modelMarketplaceTimePricingFromRouting(config *pricing.TimePricingConfig) *ModelMarketplaceTimePricing {
	if config == nil {
		return nil
	}
	periods := make([]ModelMarketplaceTimePricingPeriod, 0, len(config.Periods))
	for _, period := range config.Periods {
		periods = append(periods, ModelMarketplaceTimePricingPeriod{
			StartTime:  period.StartTime,
			EndTime:    period.EndTime,
			Multiplier: period.Multiplier,
		})
	}
	return &ModelMarketplaceTimePricing{
		Timezone:     config.Timezone,
		WeekdaysOnly: config.WeekdaysOnly,
		Periods:      periods,
	}
}

// imagePriceValue preserves a declared zero price and omits undeclared sizes.
func imagePriceValue(price pricing.ModelDisplayPricing, size string, value float64) *float64 {
	if len(price.ImagePriceSizes) > 0 {
		if !slices.Contains(price.ImagePriceSizes, size) {
			return nil
		}
	} else if value == 0 {
		return nil
	}
	return &value
}
