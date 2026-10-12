package httpapi

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// optionalBillingPrice distinguishes omitted, null, and zero prices.
type optionalBillingPrice struct{ routing.PriceUpdate }

type billingSettingsRequest struct {
	PeakRateEnabled              *bool                `json:"peak_rate_enabled"`
	PeakStart                    *string              `json:"peak_start"`
	PeakEnd                      *string              `json:"peak_end"`
	PeakRateMultiplier           *float64             `json:"peak_rate_multiplier"`
	LongContextPricingEnabled    *bool                `json:"long_context_pricing_enabled"`
	FreeOpenAIFast               *bool                `json:"free_openai_fast"`
	BatchImageDiscountMultiplier *float64             `json:"batch_image_discount_multiplier"`
	BatchImageHoldMultiplier     *float64             `json:"batch_image_hold_multiplier"`
	WebSearchPricePerCall        optionalBillingPrice `json:"web_search_price_per_call"`
	SearchPricePer1k             optionalBillingPrice `json:"search_price_per_1k"`
	AudioRealtimePricePerMin     optionalBillingPrice `json:"audio_realtime_price_per_min"`
	AudioTTSPricePerMillionChars optionalBillingPrice `json:"audio_tts_price_per_million_chars"`
	AudioSTTPricePerHour         optionalBillingPrice `json:"audio_stt_price_per_hour"`
}

func (p *optionalBillingPrice) UnmarshalJSON(data []byte) error {
	p.Set = true
	return json.Unmarshal(data, &p.Value)
}

func (r billingSettingsRequest) patch() routing.BillingSettingsPatch {
	return routing.BillingSettingsPatch{
		PeakRateEnabled:              r.PeakRateEnabled,
		PeakStart:                    r.PeakStart,
		PeakEnd:                      r.PeakEnd,
		PeakRateMultiplier:           r.PeakRateMultiplier,
		LongContextPricingEnabled:    r.LongContextPricingEnabled,
		FreeOpenAIFast:               r.FreeOpenAIFast,
		BatchImageDiscountMultiplier: r.BatchImageDiscountMultiplier,
		BatchImageHoldMultiplier:     r.BatchImageHoldMultiplier,
		WebSearchPricePerCall:        r.WebSearchPricePerCall.PriceUpdate,
		SearchPricePer1k:             r.SearchPricePer1k.PriceUpdate,
		AudioRealtimePricePerMin:     r.AudioRealtimePricePerMin.PriceUpdate,
		AudioTTSPricePerMillionChars: r.AudioTTSPricePerMillionChars.PriceUpdate,
		AudioSTTPricePerHour:         r.AudioSTTPricePerHour.PriceUpdate,
	}
}
