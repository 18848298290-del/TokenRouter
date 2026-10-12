package pricing

import (
	"strings"
	"time"
)

// BillingSettings holds shared pricing policy applied independently of whether a model card matches.
type BillingSettings struct {
	PeakRateEnabled              bool     `json:"peak_rate_enabled"`
	PeakStart                    string   `json:"peak_start"`
	PeakEnd                      string   `json:"peak_end"`
	PeakRateMultiplier           float64  `json:"peak_rate_multiplier"`
	LongContextPricingEnabled    bool     `json:"long_context_pricing_enabled"`
	FreeOpenAIFast               bool     `json:"free_openai_fast"`
	BatchImageDiscountMultiplier float64  `json:"batch_image_discount_multiplier"`
	BatchImageHoldMultiplier     float64  `json:"batch_image_hold_multiplier"`
	WebSearchPricePerCall        *float64 `json:"web_search_price_per_call"`
	SearchPricePer1k             *float64 `json:"search_price_per_1k"`
	AudioRealtimePricePerMin     *float64 `json:"audio_realtime_price_per_min"`
	AudioTTSPricePerMillionChars *float64 `json:"audio_tts_price_per_million_chars"`
	AudioSTTPricePerHour         *float64 `json:"audio_stt_price_per_hour"`
}

// DefaultBillingSettings returns policy defaults for an unconfigured group.
func DefaultBillingSettings() BillingSettings {
	return BillingSettings{
		PeakRateMultiplier:           1,
		LongContextPricingEnabled:    true,
		BatchImageDiscountMultiplier: 0.5,
		BatchImageHoldMultiplier:     0.6,
	}
}

// Clone copies optional prices so callers cannot mutate cached amounts.
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

// parseMinutes parses a time as minutes since midnight (0..1439), returning false for invalid input.
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
	return hour*60 + minute, true
}

// PeakMultiplierAt returns the peak multiplier for now's local clock time.
//   - Disabled, absent, invalid, or inactive windows return 1.
//   - The window is [PeakStart, PeakEnd), with both endpoints on the same day.
//   - Callers convert now to the billing timezone before calling.
//
// The method uses the supplied settings and time without external reads.
func (s *BillingSettings) PeakMultiplierAt(now time.Time) float64 {
	if s == nil || !s.PeakRateEnabled || s.PeakStart == "" || s.PeakEnd == "" {
		return 1.0
	}
	start, ok1 := parseMinutes(s.PeakStart)
	end, ok2 := parseMinutes(s.PeakEnd)
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
