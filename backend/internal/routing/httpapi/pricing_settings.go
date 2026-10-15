package httpapi

import (
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// pricing_settings.go decodes optional billing prices and constructs partial
// billing-settings updates.
// Operation price updates need three meanings: omit a field to retain its
// override, send null to clear it, or send zero to make the operation free. A
// numeric pointer alone merges omission and null, so optionalBillingPrice
// records presence separately while the JSON decoder keeps the optional
// amount.
//
// billingSettingsRequest passes this presence and the remaining optional
// settings into BillingSettingsPatch. Creation applies the patch to default
// settings and updates apply it to stored settings; the patch consumer
// validates the resulting values. Ordinary pointer settings retain omissions,
// and operation-price updates can still distinguish a clearing request from
// an unchanged value.

// optionalBillingPrice retains JSON field presence alongside a nullable
// operation amount.
type optionalBillingPrice struct {
	routing.PriceUpdate // supplied presence and amount
}

// billingSettingsRequest decodes optional shared billing policies and
// operation-price overrides.
type billingSettingsRequest struct {
	// PeakRateEnabled, PeakStart, PeakEnd, and PeakRateMultiplier update one
	// daily peak window. Nil fields retain existing settings. Apply checks the
	// combined factor and, when enabled, increasing same-day H:MM or HH:MM
	// clock values.
	PeakRateEnabled    *bool    `json:"peak_rate_enabled"`    // peak window activation patch
	PeakStart          *string  `json:"peak_start"`           // inclusive local peak start
	PeakEnd            *string  `json:"peak_end"`             // exclusive local peak end
	PeakRateMultiplier *float64 `json:"peak_rate_multiplier"` // peak token price factor
	// LongContextPricingEnabled controls catalog context rules; configured
	// price-card intervals remain applicable when the supplied setting is false.
	LongContextPricingEnabled *bool `json:"long_context_pricing_enabled"` // built-in context pricing patch
	// FreeOpenAIFast selects Standard funding amounts for eligible OpenAI Fast
	// usage while reported Fast costs retain their own calculation.
	FreeOpenAIFast *bool `json:"free_openai_fast"` // OpenAI Fast funding patch
	// BatchImageDiscountMultiplier and BatchImageHoldMultiplier scale batch
	// settlement and reservation amounts. Nil retains the current factors;
	// BillingSettingsPatch.Apply rejects a hold factor smaller than the discount
	// factor.
	BatchImageDiscountMultiplier *float64 `json:"batch_image_discount_multiplier"` // batch settlement discount factor
	BatchImageHoldMultiplier     *float64 `json:"batch_image_hold_multiplier"`     // batch reservation amount factor
	// WebSearchPricePerCall, SearchPricePer1k, AudioRealtimePricePerMin,
	// AudioTTSPricePerMillionChars, and AudioSTTPricePerHour retain
	// supplied-field presence. Omission keeps the existing override, null clears
	// it, and numeric zero supplies free operation pricing.
	WebSearchPricePerCall        optionalBillingPrice `json:"web_search_price_per_call"`         // USD per web-search call
	SearchPricePer1k             optionalBillingPrice `json:"search_price_per_1k"`               // USD per thousand search calls
	AudioRealtimePricePerMin     optionalBillingPrice `json:"audio_realtime_price_per_min"`      // USD per Realtime minute
	AudioTTSPricePerMillionChars optionalBillingPrice `json:"audio_tts_price_per_million_chars"` // USD per million TTS characters
	AudioSTTPricePerHour         optionalBillingPrice `json:"audio_stt_price_per_hour"`          // USD per STT hour
}

// UnmarshalJSON records that an operation price was supplied and decodes its
// optional numeric value. JSON null leaves Value nil, zero creates a nonnil
// zero, and omission bypasses this method so a fresh request retains
// Set=false.
//
// Set becomes true before decoding, including when JSON decoding returns an
// error; handlers discard such invalid requests. The presence flag lets
// BillingSettingsPatch.Apply clear overrides without confusing that action
// with leaving stored settings unchanged.
func (p *optionalBillingPrice) UnmarshalJSON(data []byte) error {
	p.Set = true
	return json.Unmarshal(data, &p.Value)
}

// patch returns the supplied billing settings for application by
// BillingSettingsPatch.Apply. It retains decoded pointers and operation-price
// presence, so the consumer can distinguish an unchanged value from a cleared
// or zero override.
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
