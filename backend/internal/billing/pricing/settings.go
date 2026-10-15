package pricing

import (
	"strings"
	"time"
)

// settings.go defines group billing policies and evaluates peak-rate windows.
// Group billing policy can apply even when no model card matches. Peak windows
// use the clock components already supplied in the billing timezone, and
// operation prices use pointers so omitted values can inherit catalog defaults
// while zero denotes free usage.
//
// DefaultBillingSettings supplies long-context and batch-image policy defaults,
// and Clone detaches the optional operation amounts from cached settings.
// PeakMultiplierAt accepts one nonwrapping [start,end) window, with invalid or
// inactive windows producing one. It returns the configured peak multiplier
// directly when active; settings validation and conversion into the intended
// billing timezone belong to its callers.

// BillingSettings contains group billing policy that applies independently of a
// matching model card. PeakRateEnabled activates the [PeakStart,PeakEnd) daily
// window, and PeakRateMultiplier supplies its factor. The caller chooses the
// timezone before evaluating those clock values.
//
// WebSearchPricePerCall, SearchPricePer1k, AudioRealtimePricePerMin,
// AudioTTSPricePerMillionChars, and AudioSTTPricePerHour retain optional amount
// presence. Billing consumers resolve nil prices from operation defaults;
// supplied zero remains free.
type BillingSettings struct {
	// daily peak-window selection flag
	PeakRateEnabled bool `json:"peak_rate_enabled"`
	// inclusive minute-resolution peak start
	PeakStart string `json:"peak_start"`
	PeakEnd   string `json:"peak_end"` // exclusive minute-resolution peak end
	// PeakRateMultiplier is returned as stored when the daily window matches.
	// Callers validate a finite supported factor before multiplying token billing
	// rates.
	// active peak-window scaling factor
	PeakRateMultiplier float64 `json:"peak_rate_multiplier"`
	// LongContextPricingEnabled permits built-in catalog thresholds and absolute
	// tiers. Configured token intervals remain effective when this policy is false
	// because they are card prices.
	// built-in context-pricing selection flag
	LongContextPricingEnabled bool `json:"long_context_pricing_enabled"`
	// FreeOpenAIFast lets eligible OpenAI Fast funding use Standard amounts in
	// billing consumers. Fast total-cost reporting and provider-cost calculation
	// retain their own prices.
	// eligible OpenAI Fast funding policy
	FreeOpenAIFast bool `json:"free_openai_fast"`
	// batch-image estimated cost discount factor
	BatchImageDiscountMultiplier float64 `json:"batch_image_discount_multiplier"`
	// batch-image reservation scaling factor
	BatchImageHoldMultiplier float64 `json:"batch_image_hold_multiplier"`
	// USD per web-search call
	WebSearchPricePerCall *float64 `json:"web_search_price_per_call"`
	// USD per thousand search calls
	SearchPricePer1k *float64 `json:"search_price_per_1k"`
	// USD per Realtime minute
	AudioRealtimePricePerMin *float64 `json:"audio_realtime_price_per_min"`
	// USD per million TTS characters
	AudioTTSPricePerMillionChars *float64 `json:"audio_tts_price_per_million_chars"`
	// USD per STT hour
	AudioSTTPricePerHour *float64 `json:"audio_stt_price_per_hour"`
}

// DefaultBillingSettings returns policy values for an unconfigured billing
// group. Long-context pricing is enabled, peak and free-Fast policy are
// disabled, and batch-image discount and hold factors are 0.5 and 0.6. Optional
// operation prices remain absent.
func DefaultBillingSettings() BillingSettings {
	return BillingSettings{
		PeakRateMultiplier:           1,
		LongContextPricingEnabled:    true,
		BatchImageDiscountMultiplier: 0.5,
		BatchImageHoldMultiplier:     0.6,
	}
}

// Clone returns settings with separate storage for all five optional prices.
// Nil and explicit zero amounts retain their meanings, so callers can modify
// copied amounts without altering the cached settings they read.
func (s BillingSettings) Clone() BillingSettings {
	if s.WebSearchPricePerCall != nil {
		v := *s.WebSearchPricePerCall
		s.WebSearchPricePerCall = &v
	}
	if s.SearchPricePer1k != nil {
		v := *s.SearchPricePer1k
		s.SearchPricePer1k = &v
	}
	if s.AudioRealtimePricePerMin != nil {
		v := *s.AudioRealtimePricePerMin
		s.AudioRealtimePricePerMin = &v
	}
	if s.AudioTTSPricePerMillionChars != nil {
		v := *s.AudioTTSPricePerMillionChars
		s.AudioTTSPricePerMillionChars = &v
	}
	if s.AudioSTTPricePerHour != nil {
		v := *s.AudioSTTPricePerHour
		s.AudioSTTPricePerHour = &v
	}
	return s
}

// parseMinutes parses a supplied clock as minutes since midnight. Hours accept
// one or two digits and minutes require two; surrounding whitespace, malformed
// digits, and values outside 00:00 through 23:59 return false.
func parseMinutes(hhmm string) (int, bool) {
	// Parse directly on the billing hot path. As with time.Parse("15:04", s),
	// hours accept one or two digits (0..23), and minutes require two digits (00..59).
	colon := strings.IndexByte(hhmm, ':')
	if (colon != 1 && colon != 2) || len(hhmm)-colon-1 != 2 {
		return 0, false
	}
	hour := 0
	for i := range colon {
		digit := hhmm[i] - '0'
		if digit > 9 {
			return 0, false
		}
		hour = hour*10 + int(digit)
	}
	minuteTens, minuteOnes := hhmm[colon+1]-'0', hhmm[colon+2]-'0'
	if minuteTens > 9 || minuteOnes > 9 {
		return 0, false
	}
	minute := int(minuteTens)*10 + int(minuteOnes)
	if hour > 23 || minute > 59 {
		return 0, false
	}
	// Use minutes because peak window comparisons ignore clock seconds.
	return hour*60 + minute, true
}

// PeakMultiplierAt returns the configured multiplier within the active daily
// peak window. Callers supply now in the intended billing timezone and validate
// the multiplier beforehand. Disabled, missing, malformed, wrapping, or
// inactive windows return one.
//
// The comparison uses minutes and a [PeakStart,PeakEnd) range, so seconds
// within the ending minute are outside the peak. The clock's date and weekday
// do not influence this window, and a matching window returns
// PeakRateMultiplier as stored.
func (s *BillingSettings) PeakMultiplierAt(now time.Time) float64 {
	if s == nil || !s.PeakRateEnabled || s.PeakStart == "" || s.PeakEnd == "" {
		return 1.0
	}
	start, ok1 := parseMinutes(s.PeakStart)
	end, ok2 := parseMinutes(s.PeakEnd)
	// Retain the unit factor when either clock is malformed or the range fails to
	// advance; cached settings then have no active peak window.
	if !ok1 || !ok2 || start >= end {
		return 1.0
	}
	t := now
	cur := t.Hour()*60 + t.Minute()
	if cur >= start && cur < end {
		return s.PeakRateMultiplier
	}
	return 1.0
}
