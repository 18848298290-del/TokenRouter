package routing

// This scenario checks peak multipliers in billing/pricing/settings.go; pricing_settings.go validates configuration.

import (
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

func TestPeakMultiplierAt_DisabledOrUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		g    *pricing.BillingSettings
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

func TestPeakMultiplierAt_NilReceiver(t *testing.T) {
	var g *pricing.BillingSettings
	if got := g.PeakMultiplierAt(at(15, 0)); got != 1.0 {
		t.Fatalf("expect 1.0, got %v", got)
	}
}

func TestPeakMultiplierAt_Boundaries(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	cases := []struct {
		t    time.Time
		want float64
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

func TestPeakMultiplierAt_RespectsTimezoneLocation(t *testing.T) {
	// Peak hours use UTC: 15:00 in Beijing is 07:00 UTC, outside [14:00, 18:00).
	nonUTC := time.Date(2026, 6, 29, 15, 0, 0, 0, mustLoad("Asia/Shanghai"))
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	if got := g.PeakMultiplierAt(nonUTC.In(time.UTC)); got != 1.0 {
		t.Fatalf("expect 1.0 (converted to UTC 07:00), got %v", got)
	}
}

func TestPeakMultiplierAt_EnabledGroupUsesConfiguredWindow(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	if got := g.PeakMultiplierAt(at(15, 30)); got != 3.0 {
		t.Fatalf("enabled group peak multiplier: got %v, want 3.0", got)
	}
}

func newPeakGroup(enabled bool, start, end string, mult float64) *pricing.BillingSettings {
	return &pricing.BillingSettings{
		PeakRateEnabled:    enabled,
		PeakStart:          start,
		PeakEnd:            end,
		PeakRateMultiplier: mult,
	}
}

func at(hour, min int) time.Time {
	return time.Date(2026, 6, 29, hour, min, 0, 0, time.UTC)
}

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}
