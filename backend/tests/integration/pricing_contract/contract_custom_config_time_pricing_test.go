package pricingcontract

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/billing/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Daily token pricing uses validated wall-clock periods evaluated in a
// supplied location. These fixtures connect routing validation with the pure
// multiplier calculation, covering adjoining periods, midnight splits, weekday
// selection, and a unit multiplier when timezone loading cannot
// supply a location.

// TestValidateTimePricingConfig supplies disabled schedules, adjacent periods,
// and malformed daily pricing rules. It checks successful validation or the
// expected error fragment for timezone, clock, overlap, precision,
// and finite-multiplier cases.
func TestValidateTimePricingConfig(t *testing.T) {
	tests := []struct {
		name    string                     // subtest selection label
		config  *routing.TimePricingConfig // daily pricing schedule
		wantErr string                     // expected validation error fragment
	}{
		{name: "nil disabled"},
		{name: "empty disabled", config: pricingTimePricingTestConfig()},
		{name: "adjacent", config: pricingTimePricingTestConfig(
			routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: 2},
			routing.TimePricingPeriod{StartTime: "12:00", EndTime: "14:00", Multiplier: 1.5},
		)},
		{name: "midnight split", config: pricingTimePricingTestConfig(
			routing.TimePricingPeriod{StartTime: "22:00", EndTime: "00:00", Multiplier: 2},
			routing.TimePricingPeriod{StartTime: "00:00", EndTime: "02:00", Multiplier: 2},
		)},
		{name: "empty timezone", config: &routing.TimePricingConfig{Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}}, wantErr: "timezone"},
		{name: "invalid timezone", config: &routing.TimePricingConfig{Timezone: "UTC+8", Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}}, wantErr: "timezone"},
		{name: "invalid format", config: pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "9:00", EndTime: "12:00", Multiplier: 2}), wantErr: "HH:mm"},
		{name: "cross midnight", config: pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "22:00", EndTime: "02:00", Multiplier: 2}), wantErr: "before"},
		{name: "overlap", config: pricingTimePricingTestConfig(
			routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: 2},
			routing.TimePricingPeriod{StartTime: "11:59", EndTime: "14:00", Multiplier: 2},
		), wantErr: "overlap"},
		{name: "zero multiplier", config: pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: 0}), wantErr: "greater than 0"},
		{name: "minimum multiplier", config: pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: 0.01})},
		{name: "too many decimals", config: pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: 1.001}), wantErr: "decimal"},
		{name: "overflow", config: pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: math.MaxFloat64}), wantErr: "finite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (routing.PricingConfigValidation{LoadLocation: provider.LoadPricingLocation}).TimePricing(tt.config)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestTimePricingConfigMultiplierAt evaluates one daily period at its start,
// end, and preceding minute. It checks the Shanghai endpoints and a New York
// local-time match for UTC instants.
func TestTimePricingConfigMultiplierAt(t *testing.T) {
	config := pricingTimePricingTestConfig(routing.TimePricingPeriod{StartTime: "09:00", EndTime: "12:00", Multiplier: 2})
	require.Equal(t, 1.0, config.MultiplierAt(time.Date(2026, 6, 29, 0, 59, 0, 0, time.UTC), contractTimeLocation(config)))
	require.Equal(t, 2.0, config.MultiplierAt(time.Date(2026, 6, 29, 1, 0, 0, 0, time.UTC), contractTimeLocation(config)))
	require.Equal(t, 1.0, config.MultiplierAt(time.Date(2026, 6, 29, 4, 0, 0, 0, time.UTC), contractTimeLocation(config)))

	newYork := &routing.TimePricingConfig{Timezone: "America/New_York", Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}}
	require.Equal(t, 2.0, newYork.MultiplierAt(time.Date(2026, 6, 29, 14, 0, 0, 0, time.UTC), contractTimeLocation(newYork)))
}

// TestTimePricingConfigMultiplierAtWeekdaysOnly evaluates the same Shanghai
// morning period on Monday, Saturday, and Sunday. It checks that the
// configured factor applies on the weekday and becomes one on weekends.
func TestTimePricingConfigMultiplierAtWeekdaysOnly(t *testing.T) {
	config := &routing.TimePricingConfig{
		Timezone:     "Asia/Shanghai",
		WeekdaysOnly: true,
		Periods: []routing.TimePricingPeriod{{
			StartTime: "09:00", EndTime: "12:00", Multiplier: 2,
		}},
	}

	tests := []struct {
		name string    // subtest selection label
		at   time.Time // UTC evaluation instant
		want float64   // expected dimensionless pricing factor
	}{
		{name: "Monday in configured timezone", at: time.Date(2026, 6, 29, 1, 0, 0, 0, time.UTC), want: 2},
		{name: "Saturday in configured timezone", at: time.Date(2026, 7, 4, 1, 0, 0, 0, time.UTC), want: 1},
		{name: "Sunday in configured timezone", at: time.Date(2026, 7, 5, 1, 0, 0, 0, time.UTC), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, config.MultiplierAt(tt.at, contractTimeLocation(config)))
		})
	}
}

// TestTimePricingConfigMultiplierAtMidnightSplit supplies separate
// late-evening and early-morning periods with different factors. It checks the
// factor immediately before midnight, at midnight, and at the
// exclusive morning endpoint.
func TestTimePricingConfigMultiplierAtMidnightSplit(t *testing.T) {
	config := pricingTimePricingTestConfig(
		routing.TimePricingPeriod{StartTime: "22:00", EndTime: "00:00", Multiplier: 2},
		routing.TimePricingPeriod{StartTime: "00:00", EndTime: "02:00", Multiplier: 3},
	)
	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	require.Equal(t, 2.0, config.MultiplierAt(time.Date(2026, 6, 29, 23, 59, 0, 0, location), contractTimeLocation(config)))
	require.Equal(t, 3.0, config.MultiplierAt(time.Date(2026, 6, 30, 0, 0, 0, 0, location), contractTimeLocation(config)))
	require.Equal(t, 1.0, config.MultiplierAt(time.Date(2026, 6, 30, 2, 0, 0, 0, location), contractTimeLocation(config)))
}

// TestTimePricingConfigMultiplierAtDegradesForInvalidConfiguration evaluates
// nil schedules and schedules whose timezone cannot be loaded. It checks a
// unit factor for those inputs and a validation error for the
// process-dependent Local name.
func TestTimePricingConfigMultiplierAtDegradesForInvalidConfiguration(t *testing.T) {
	var nilConfig *routing.TimePricingConfig
	validAt := time.Date(2026, 6, 29, 1, 0, 0, 0, time.UTC)
	require.Equal(t, 1.0, nilConfig.MultiplierAt(validAt, contractTimeLocation(nilConfig)))
	require.Equal(t, 1.0, (&routing.TimePricingConfig{Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}}).MultiplierAt(validAt, contractTimeLocation(&routing.TimePricingConfig{Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}})))
	require.Equal(t, 1.0, (&routing.TimePricingConfig{Timezone: "UTC+8", Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}}).MultiplierAt(validAt, contractTimeLocation(&routing.TimePricingConfig{Timezone: "UTC+8", Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}})))

	err := (routing.PricingConfigValidation{LoadLocation: provider.LoadPricingLocation}).TimePricing(&routing.TimePricingConfig{Timezone: "Local", Periods: []routing.TimePricingPeriod{{StartTime: "09:00", EndTime: "12:00", Multiplier: 2}}})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "timezone"))
}

// pricingTimePricingTestConfig constructs a Shanghai daily schedule from
// periods. The returned schedule retains the supplied slice, so callers keep
// its elements stable while validating or evaluating that schedule. An empty slice
// disables time pricing.
func pricingTimePricingTestConfig(periods ...routing.TimePricingPeriod) *routing.TimePricingConfig {
	return &routing.TimePricingConfig{Timezone: "Asia/Shanghai", Periods: periods}
}

// contractTimeLocation loads the schedule's named timezone for multiplier
// assertions. Nil schedules return nil, and load errors are discarded so
// invalid timezone fixtures reach MultiplierAt with a missing location.
func contractTimeLocation(value *pricing.TimePricingConfig) *time.Location {
	if value == nil {
		return nil
	}
	// Invalid timezone fixtures intentionally discard the loading error. The
	// resulting nil location lets MultiplierAt exercise its unit-factor path
	// during these assertions.
	location, _ := provider.LoadPricingLocation(value.Timezone)
	return location
}
