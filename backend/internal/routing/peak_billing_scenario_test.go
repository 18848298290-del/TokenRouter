package routing

import (
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Peak windows in billing/pricing/settings.go compare the clock components
// supplied by their caller, while pricing_settings.go validates saved
// windows. These scenarios cover inactive or malformed windows, minute
// boundaries, and caller-side timezone conversion before evaluating one
// configured multiplier.

// TestPeakMultiplierAt_DisabledOrUnconfigured evaluates an afternoon instant
// against disabled, missing, malformed, equal, and wrapping window settings.
// Every supplied case returns the neutral multiplier.
func TestPeakMultiplierAt_DisabledOrUnconfigured(t *testing.T) {
	cases := []struct {
		name string                   // subtest window case label
		g    *pricing.BillingSettings // supplied peak billing settings
	}{
		{"disabled", newPeakGroup(false, "14:00", "18:00", 3.0)},
		{"empty start", newPeakGroup(true, "", "18:00", 3.0)},
		{"empty end", newPeakGroup(true, "14:00", "", 3.0)},
		{"invalid start>=end", newPeakGroup(true, "18:00", "14:00", 3.0)},
		{"equal start==end", newPeakGroup(true, "14:00", "14:00", 3.0)},
		{"malformed start", newPeakGroup(true, "99:99", "18:00", 3.0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.g.PeakMultiplierAt(at(15, 0)); got != 1.0 {
				t.Fatalf("expect 1.0, got %v", got)
			}
		})
	}
}

// TestPeakMultiplierAt_NilReceiver evaluates an afternoon instant through a
// nil billing-settings pointer. The method returns the neutral multiplier so
// missing settings can be evaluated safely.
func TestPeakMultiplierAt_NilReceiver(t *testing.T) {
	var g *pricing.BillingSettings
	if got := g.PeakMultiplierAt(at(15, 0)); got != 1.0 {
		t.Fatalf("expect 1.0, got %v", got)
	}
}

// TestPeakMultiplierAt_Boundaries samples minutes before, inside, and after a
// 14:00 to 18:00 window. The start is included, the end is excluded, and
// outside minutes use one.
func TestPeakMultiplierAt_Boundaries(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	cases := []struct {
		t    time.Time // sampled UTC clock instant
		want float64   // expected peak multiplier result
	}{
		{at(13, 59), 1.0},
		{at(14, 0), 3.0},
		{at(15, 30), 3.0},
		{at(17, 59), 3.0},
		{at(18, 0), 1.0},
		{at(23, 0), 1.0},
	}
	for _, c := range cases {
		t.Run(c.t.Format("15:04"), func(t *testing.T) {
			if got := g.PeakMultiplierAt(c.t); got != c.want {
				t.Fatalf("at %s: expect %v, got %v", c.t.Format("15:04"), c.want, got)
			}
		})
	}
}

// TestPeakMultiplierAt_RespectsTimezoneLocation converts a Shanghai afternoon
// instant to UTC before calling the multiplier method. Its UTC morning clock
// lies outside the configured afternoon window and returns one.
func TestPeakMultiplierAt_RespectsTimezoneLocation(t *testing.T) {
	// Convert the Shanghai instant before evaluation because PeakMultiplierAt
	// compares the supplied timestamp's clock components; 15:00 there becomes
	// 07:00 UTC, outside the configured afternoon window.
	nonUTC := time.Date(2026, 6, 29, 15, 0, 0, 0, mustLoad("Asia/Shanghai"))
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	if got := g.PeakMultiplierAt(nonUTC.In(time.UTC)); got != 1.0 {
		t.Fatalf("expect 1.0 (converted to UTC 07:00), got %v", got)
	}
}

// TestPeakMultiplierAt_EnabledGroupUsesConfiguredWindow evaluates a UTC
// instant inside an enabled afternoon window. The result equals the stored
// multiplier supplied by the fixture.
func TestPeakMultiplierAt_EnabledGroupUsesConfiguredWindow(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	if got := g.PeakMultiplierAt(at(15, 30)); got != 3.0 {
		t.Fatalf("enabled group peak multiplier: got %v, want 3.0", got)
	}
}

// newPeakGroup returns settings with the supplied peak flag, clocks, and
// multiplier.
func newPeakGroup(enabled bool, start, end string, mult float64) *pricing.BillingSettings {
	return &pricing.BillingSettings{
		PeakRateEnabled:    enabled,
		PeakStart:          start,
		PeakEnd:            end,
		PeakRateMultiplier: mult,
	}
}

// at returns a fixed-date UTC timestamp for the supplied hour and minute.
func at(hour, min int) time.Time {
	return time.Date(2026, 6, 29, hour, min, 0, 0, time.UTC)
}

// mustLoad returns the requested timezone or panics when its data is
// unavailable. Tests use it for named-location fixtures, so missing timezone
// support cannot silently change their timestamps.
func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}
