package pricing

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Daily token-price periods use wall-clock seconds in a caller-supplied
// location. The same instant can select a different period in a different zone;
// repeated daylight-saving hours each match the period for their displayed
// local clock time.
//
// Period parsing validates finite positive multipliers, two-decimal precision,
// and nonoverlapping left-closed ranges before lookup. End-of-day midnight is
// represented as 86400 seconds, while ordinary midnight starts at zero.
// MultiplierAt returns one for disabled rules, invalid periods, a zero instant,
// or a missing location, and applies weekday restrictions after converting the
// instant. The configuration's zone name is validated and loaded by callers.

// ParsedPricingTimePeriod contains a validated daily wall-clock range and
// factor. start is inclusive and end is exclusive in seconds since midnight;
// end can reach 86400 for the day's final range. multiplier has passed the
// period amount and precision checks.
type ParsedPricingTimePeriod struct {
	start      int     // inclusive seconds since midnight
	end        int     // exclusive seconds since midnight
	multiplier float64 // validated daily token-price factor
}

// ValidateTimePricingConfig checks the configuration's period values and
// ordering. Nil configurations and empty periods succeed because they disable
// the schedule. Timezone syntax and location loading are separate caller
// checks.
func ValidateTimePricingConfig(config *TimePricingConfig) error {
	if config == nil || len(config.Periods) == 0 {
		return nil
	}
	_, err := ParsePricingTimePeriods(config.Periods)
	return err
}

// MultiplierAt returns the matching daily multiplier for at in location. Nil or
// empty rules, a zero instant, invalid periods, and nil locations return one.
// Periods use [start,end), and weekday restrictions use the converted local
// date.
//
// The supplied location controls conversion; Timezone does not load a location
// here. Repeated local hours can each match the same wall-clock period, while
// time outside every configured period receives the unit multiplier.
func (config *TimePricingConfig) MultiplierAt(at time.Time, location *time.Location) float64 {
	if config == nil || len(config.Periods) == 0 || at.IsZero() {
		return 1.0
	}
	// Invalid period data is treated as an inactive schedule, so a corrupt cached
	// rule cannot select a surcharge.
	if err := ValidateTimePricingConfig(config); err != nil {
		return 1.0
	}
	if location == nil {
		return 1.0
	}
	// Parsing errors retain the unit multiplier; lookup proceeds only with a
	// complete validated set of local-clock periods.
	periods, err := ParsePricingTimePeriods(config.Periods)
	if err != nil {
		return 1.0
	}

	local := at.In(location)
	if config.WeekdaysOnly && (local.Weekday() == time.Saturday || local.Weekday() == time.Sunday) {
		return 1.0
	}
	// Convert the supplied local clock into period comparison seconds.
	second := local.Hour()*60*60 + local.Minute()*60 + local.Second()
	for _, period := range periods {
		if second >= period.start && second < period.end {
			return period.multiplier
		}
	}
	return 1.0
}

// ParsePricingTime parses an exact HH:mm or HH:mm:ss clock into seconds. End
// values spelled 00:00 or 00:00:00 represent 86400; start midnight is zero.
// Invalid or noncanonical clock text returns an error.
func ParsePricingTime(value string, end bool) (int, error) {
	if end && (value == "00:00" || value == "00:00:00") {
		// End midnight closes the current day's final second range.
		return 24 * 60 * 60, nil
	}
	layout := "15:04:05"
	if len(value) == len("15:04") {
		layout = "15:04"
	}
	parsed, err := time.Parse(layout, value)
	if err != nil || parsed.Format(layout) != value {
		return 0, fmt.Errorf("time %q must use HH:mm or HH:mm:ss format", value)
	}
	// Convert clock components into the range comparison's second unit.
	return parsed.Hour()*60*60 + parsed.Minute()*60 + parsed.Second(), nil
}

// ParsePricingTimePeriods returns newly allocated periods sorted by their start
// seconds. Multipliers must be finite, positive, at least 0.01, and within the
// scaled precision tolerance. Clocks use exact supported formats, identical
// endpoint text is rejected, and ranges must advance within one day.
//
// The sorted result rejects overlapping ranges but accepts adjoining endpoints
// because the end is exclusive. Validation leaves the supplied periods
// unchanged and returns no partial parsed slice when any field or overlap check
// fails.
func ParsePricingTimePeriods(periods []TimePricingPeriod) ([]ParsedPricingTimePeriod, error) {
	parsed := make([]ParsedPricingTimePeriod, 0, len(periods))
	for _, period := range periods {
		if math.IsNaN(period.Multiplier) || math.IsInf(period.Multiplier, 0) || period.Multiplier <= 0 {
			return nil, fmt.Errorf("multiplier must be finite and greater than 0")
		}
		if period.Multiplier < 0.01 {
			return nil, fmt.Errorf("multiplier must be at least 0.01")
		}
		// Scale by cents to check the configured decimal precision.
		scaled := period.Multiplier * 100
		if math.IsNaN(scaled) || math.IsInf(scaled, 0) {
			return nil, fmt.Errorf("multiplier must remain finite when scaled")
		}
		// Allow a small binary rounding residue after hundredfold scaling.
		if math.Abs(scaled-math.Round(scaled)) > 1e-9 {
			return nil, fmt.Errorf("multiplier must have at most two decimal places")
		}

		start, err := ParsePricingTime(period.StartTime, false)
		if err != nil {
			return nil, err
		}
		end, err := ParsePricingTime(period.EndTime, true)
		if err != nil {
			return nil, err
		}
		if period.StartTime == period.EndTime || start >= end {
			return nil, fmt.Errorf("start time must be before end time")
		}
		parsed = append(parsed, ParsedPricingTimePeriod{start: start, end: end, multiplier: period.Multiplier})
	}

	sort.Slice(parsed, func(i, j int) bool { return parsed[i].start < parsed[j].start })
	for i := 1; i < len(parsed); i++ {
		if parsed[i].start < parsed[i-1].end {
			return nil, fmt.Errorf("time pricing periods overlap")
		}
	}
	return parsed, nil
}

// ValidateTimezoneName rejects whitespace-only names and the exact name Local.
// Other strings pass this syntactic check as supplied, including surrounding
// whitespace. Callers use time.LoadLocation separately to validate and load the
// actual zone.
func ValidateTimezoneName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("timezone is required")
	}
	if name == "Local" {
		return fmt.Errorf("local is not a supported timezone")
	}
	return nil
}

// ResolvedTimeMultiplier evaluates a resolved token card's daily schedule.
// Other modes and absent cards return one; the card's MultiplierAt also handles
// missing rules, invalid periods, zero instants, and nil locations.
func ResolvedTimeMultiplier(resolved *ResolvedPricing, at time.Time, location *time.Location) float64 {
	if resolved == nil || resolved.Mode != BillingModeToken || resolved.ConfigPricing == nil {
		return 1
	}
	return resolved.ConfigPricing.TimePricing.MultiplierAt(at, location)
}
