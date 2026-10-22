package routing

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// pricing_settings.go merges partial billing settings and validates the
// resulting prices and peak windows.
// Billing settings updates distinguish an omitted optional price, a cleared
// override, and an explicit free amount. PriceUpdate carries presence
// separately from its amount pointer, while ordinary pointer settings allow
// partial changes to be merged with the current configuration.
//
// Apply checks the merged peak window, finite nonnegative amounts, and batch
// hold versus discount factors before copying accepted amount pointers.
// Assignments occur before validation, so an error can leave the supplied
// working settings changed and still sharing supplied price pointers. Callers
// use a disposable working value until acceptance. Disabled peak settings
// retain valid clocks for re-enablement, while the active window requires
// increasing same-day minute values.

// PriceUpdate distinguishes omitted optional amounts, cleared overrides, and
// supplied free prices.
type PriceUpdate struct {
	// Set and Value preserve optional-price intent. Set=false retains the stored
	// amount; Set=true with nil Value clears it, and a pointer to zero supplies
	// free pricing.
	Set   bool     // optional update presence marker
	Value *float64 // supplied optional price amount
}

// BillingSettingsPatch contains optional changes merged into current shared
// billing settings.
type BillingSettingsPatch struct {
	// PeakRateEnabled, PeakStart, PeakEnd, and PeakRateMultiplier apply supplied
	// pointer values to one saved peak window. Nil retains each field, and Apply
	// validates their merged configuration together. An enabled window requires
	// valid same-day clocks with PeakEnd after PeakStart.
	PeakRateEnabled    *bool    // enable daily peak multiplier
	PeakStart          *string  // peak window start clock
	PeakEnd            *string  // peak window end clock
	PeakRateMultiplier *float64 // peak-window amount scaling factor
	// LongContextPricingEnabled, FreeOpenAIFast, BatchImageDiscountMultiplier, and
	// BatchImageHoldMultiplier retain omitted pointer values. Supplied false and
	// zero remain present, and Apply checks discount and hold factors together.
	LongContextPricingEnabled    *bool    // enable built-in long-context pricing
	FreeOpenAIFast               *bool    // charge eligible Fast at Standard
	BatchImageDiscountMultiplier *float64 // batch image settlement scaling factor
	// BatchImageHoldMultiplier cannot be below the merged discount factor. A
	// smaller value rejects Apply even when the patch did not supply the existing
	// discount.
	BatchImageHoldMultiplier *float64 // batch image reservation scaling factor
	// WebSearchPricePerCall, SearchPricePer1k, AudioRealtimePricePerMin,
	// AudioTTSPricePerMillionChars, and AudioSTTPricePerHour use PriceUpdate
	// presence rules. Explicit zero is free, explicit nil clears an override, and
	// omitted updates retain it.
	WebSearchPricePerCall        PriceUpdate // USD per web-search call
	SearchPricePer1k             PriceUpdate // USD per thousand searches
	AudioRealtimePricePerMin     PriceUpdate // USD per realtime audio minute
	AudioTTSPricePerMillionChars PriceUpdate // USD per million speech characters
	AudioSTTPricePerHour         PriceUpdate // USD per transcribed audio hour
}

// Apply merges supplied billing settings and validates the complete working
// result. Supply a non-nil disposable target because assignments precede
// validation and a returned error can leave fields changed.
//
// Optional amounts preserve omitted, cleared, and explicit-zero input through
// PriceUpdate. Accepted amounts and factors must be finite and nonnegative, the
// reservation factor cannot be below the discount factor, and enabled peak
// clocks must form an increasing same-day range. Only after validation does
// Clone detach the optional price pointers from input storage.
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

	// Validate the merged factors after applying supplied values, so existing
	// settings and the patch jointly satisfy amount bounds and
	// reservation-versus-settlement ordering.
	for _, v := range []float64{s.PeakRateMultiplier, s.BatchImageDiscountMultiplier, s.BatchImageHoldMultiplier} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", "Multipliers must be finite nonnegative numbers")
		}
	}
	for _, v := range []*float64{s.WebSearchPricePerCall, s.SearchPricePer1k, s.AudioRealtimePricePerMin, s.AudioTTSPricePerMillionChars, s.AudioSTTPricePerHour} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0) {
			return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", "Prices must be finite nonnegative numbers")
		}
	}
	if s.BatchImageHoldMultiplier < s.BatchImageDiscountMultiplier {
		return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", "Precharge multiplier must not be lower than the discount multiplier")
	}
	s.PeakRateEnabled, s.PeakStart, s.PeakEnd, s.PeakRateMultiplier = NormalizePeakRateConfig(s.PeakRateEnabled, s.PeakStart, s.PeakEnd, s.PeakRateMultiplier)
	if err := ValidatePeakRateConfig(s.PeakRateEnabled, s.PeakStart, s.PeakEnd, s.PeakRateMultiplier); err != nil {
		return infraerrors.BadRequest("INVALID_BILLING_SETTINGS", err.Error())
	}
	// Detach accepted optional amount pointers after validation; until this point
	// supplied pointers can remain attached to the disposable working settings on
	// an error path.
	*s = s.Clone()
	return nil
}

// ParseMinutes converts a supplied H:MM or HH:MM clock to minutes since
// midnight. It accepts one or two hour digits and exactly two minute digits,
// rejects surrounding whitespace, and returns false for malformed or
// out-of-range clocks.
func ParseMinutes(hhmm string) (int, bool) {
	// Accept the saved clock widths before digit parsing so extra digits and
	// surrounding whitespace cannot enter the minute-based peak-window comparison.
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
	// Keep both clock components within a single day's minute range.
	if hour > 23 || minute > 59 {
		return 0, false
	}
	// Peak window comparisons use minute granularity rather than seconds.
	return hour*60 + minute, true
}

// ValidatePeakRateConfig checks the active peak window's clocks and rejects a
// negative multiplier. Disabled configuration passes unchanged; enabled windows
// require valid increasing same-day times, and callers validate finite amounts
// separately when accepting external settings.
func ValidatePeakRateConfig(enabled bool, start, end string, multiplier float64) error {
	if !enabled {
		return nil
	}
	if start == "" || end == "" {
		return errors.New("peak_start and peak_end are required when peak_rate_enabled is true")
	}
	st, okStart := ParseMinutes(start)
	if !okStart {
		return fmt.Errorf("peak_start must use HH:MM format, got %q", start)
	}
	en, okEnd := ParseMinutes(end)
	if !okEnd {
		return fmt.Errorf("peak_end must use HH:MM format, got %q", end)
	}
	if st >= en {
		return errors.New("peak_end must be after peak_start; overnight ranges such as 22:00-02:00 are not supported")
	}
	if multiplier < 0 {
		return errors.New("peak_rate_multiplier must not be negative")
	}
	return nil
}

// NormalizePeakRateConfig prepares disabled peak settings for later
// re-enablement by clearing malformed clocks and resetting a negative
// multiplier to one. Enabled input is returned unchanged for validation; other
// multipliers remain as supplied, including values requiring a separate
// finiteness check.
func NormalizePeakRateConfig(enabled bool, start, end string, multiplier float64) (bool, string, string, float64) {
	if enabled {
		return enabled, start, end, multiplier
	}
	// Clear malformed disabled clocks independently while retaining any valid
	// companion value, so re-enabling the policy begins from editable saved clock
	// settings.
	if _, ok := ParseMinutes(start); !ok {
		start = ""
	}
	if _, ok := ParseMinutes(end); !ok {
		end = ""
	}
	// A disabled negative factor is replaced with the neutral multiplier so
	// re-enabling a retained valid clock range does not revive a negative amount
	// factor.
	if multiplier < 0 {
		multiplier = 1.0
	}
	return false, start, end, multiplier
}
