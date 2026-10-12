package routing

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// PriceUpdate distinguishes an omitted update, a cleared override, and an explicit zero price.
type PriceUpdate struct {
	Set   bool
	Value *float64
}

// BillingSettingsPatch applies only fields supplied by the caller.
type BillingSettingsPatch struct {
	PeakRateEnabled              *bool
	PeakStart                    *string
	PeakEnd                      *string
	PeakRateMultiplier           *float64
	LongContextPricingEnabled    *bool
	FreeOpenAIFast               *bool
	BatchImageDiscountMultiplier *float64
	BatchImageHoldMultiplier     *float64
	WebSearchPricePerCall        PriceUpdate
	SearchPricePer1k             PriceUpdate
	AudioRealtimePricePerMin     PriceUpdate
	AudioTTSPricePerMillionChars PriceUpdate
	AudioSTTPricePerHour         PriceUpdate
}

func (p BillingSettingsPatch) Apply(s *pricing.BillingSettings) error {
	if p.PeakRateEnabled != nil {
		s.PeakRateEnabled = *p.PeakRateEnabled
	}
	if p.PeakStart != nil {
		s.PeakStart = *p.PeakStart
	}
	if p.PeakEnd != nil {
		s.PeakEnd = *p.PeakEnd
	}
	if p.PeakRateMultiplier != nil {
		s.PeakRateMultiplier = *p.PeakRateMultiplier
	}
	if p.LongContextPricingEnabled != nil {
		s.LongContextPricingEnabled = *p.LongContextPricingEnabled
	}
	if p.FreeOpenAIFast != nil {
		s.FreeOpenAIFast = *p.FreeOpenAIFast
	}
	if p.BatchImageDiscountMultiplier != nil {
		s.BatchImageDiscountMultiplier = *p.BatchImageDiscountMultiplier
	}
	if p.BatchImageHoldMultiplier != nil {
		s.BatchImageHoldMultiplier = *p.BatchImageHoldMultiplier
	}
	if p.WebSearchPricePerCall.Set {
		s.WebSearchPricePerCall = p.WebSearchPricePerCall.Value
	}
	if p.SearchPricePer1k.Set {
		s.SearchPricePer1k = p.SearchPricePer1k.Value
	}
	if p.AudioRealtimePricePerMin.Set {
		s.AudioRealtimePricePerMin = p.AudioRealtimePricePerMin.Value
	}
	if p.AudioTTSPricePerMillionChars.Set {
		s.AudioTTSPricePerMillionChars = p.AudioTTSPricePerMillionChars.Value
	}
	if p.AudioSTTPricePerHour.Set {
		s.AudioSTTPricePerHour = p.AudioSTTPricePerHour.Value
	}

	for _, v := range []float64{s.PeakRateMultiplier, s.BatchImageDiscountMultiplier, s.BatchImageHoldMultiplier} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", "倍率必须为非负有限数值")
		}
	}
	for _, v := range []*float64{s.WebSearchPricePerCall, s.SearchPricePer1k, s.AudioRealtimePricePerMin, s.AudioTTSPricePerMillionChars, s.AudioSTTPricePerHour} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", "价格必须为非负有限数值")
		}
	}
	if s.BatchImageHoldMultiplier < s.BatchImageDiscountMultiplier {
		return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", "预扣倍率不能低于折扣倍率")
	}
	s.PeakRateEnabled, s.PeakStart, s.PeakEnd, s.PeakRateMultiplier = NormalizePeakRateConfig(s.PeakRateEnabled, s.PeakStart, s.PeakEnd, s.PeakRateMultiplier)
	if err := ValidatePeakRateConfig(s.PeakRateEnabled, s.PeakStart, s.PeakEnd, s.PeakRateMultiplier); err != nil {
		return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", err.Error())
	}
	*s = s.Clone()
	return nil
}

// ParseMinutes parses H:MM or HH:MM into minutes since midnight (0..1439), returning (0, false) on invalid input.
func ParseMinutes(hhmm string) (int, bool) {
	// Parse directly on the billing path using the hour/minute widths accepted by time.Parse("15:04", s).
	// Hours have one or two digits (0..23); minutes have two digits (00..59).
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

// ValidatePeakRateConfig validates peak-pricing windows for handlers and services.
// When enabled, start and end must be valid same-day times with end > start, and multiplier must be nonnegative.
// Zero is allowed for free peak-period token requests.
// Disabled configurations pass validation.
func ValidatePeakRateConfig(enabled bool, start, end string, multiplier float64) error {
	if !enabled {
		return nil
	}
	if start == "" || end == "" {
		return errors.New("peak_rate_enabled 为 true 时 peak_start 与 peak_end 必填")
	}
	st, okStart := ParseMinutes(start)
	if !okStart {
		return fmt.Errorf("peak_start 格式应为 HH:MM，got %q", start)
	}
	en, okEnd := ParseMinutes(end)
	if !okEnd {
		return fmt.Errorf("peak_end 格式应为 HH:MM，got %q", end)
	}
	if st >= en {
		return errors.New("peak_end 必须大于 peak_start（不支持跨天区间，如 22:00-02:00）")
	}
	if multiplier < 0 {
		return errors.New("peak_rate_multiplier 不能为负")
	}
	return nil
}

// NormalizePeakRateConfig prepares peak settings for persistence.
// Enabled values pass through for validation. Disabled values retain valid times and nonnegative multipliers,
// clearing malformed times and resetting a negative multiplier to 1 for later re-enablement.
func NormalizePeakRateConfig(enabled bool, start, end string, multiplier float64) (bool, string, string, float64) {
	if enabled {
		return enabled, start, end, multiplier
	}
	if _, ok := ParseMinutes(start); !ok {
		start = ""
	}
	if _, ok := ParseMinutes(end); !ok {
		end = ""
	}
	if multiplier < 0 {
		multiplier = 1.0
	}
	return false, start, end, multiplier
}
