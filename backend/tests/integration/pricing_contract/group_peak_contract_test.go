package pricingcontract

import (
	"math"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
)

// Peak settings change the token funding multiplier while images keep the
// supplied base multiplier. A fixed UTC key snapshot separates the peak window
// from the process timezone and checks peak, off-peak, and missing-key results
// through the completion helper.

// TestPeakMultiplier_GatewayBillingSequence evaluates a fixed peak window
// through the completion multiplier helper. It checks token amplification, the
// base image rate, and base rates outside the window or with a nil key.
func TestPeakMultiplier_GatewayBillingSequence(t *testing.T) {
	const baseMultiplier = 0.8
	keySnapshot := &completion.KeySnapshot{Group: newPeakGroup(true, "14:00", "18:00", 3.0)}
	// The snapshot's explicit UTC location evaluates these clock-only peak
	// settings against at's UTC instants independently of
	// the process calendar.
	keySnapshot.Group.Location = time.UTC
	// Allow a small absolute residue when comparing dimensionless factors.
	approxEq := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	t.Run("peak hour amplifies token multiplier only", func(t *testing.T) {
		now := at(15, 30) // Inside [14:00, 18:00).
		tokenMultiplier, imageMultiplier := completion.ComputePeakAwareMultipliers(keySnapshot, baseMultiplier, now)
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("image multiplier must not be affected by peak: got %v, want %v", imageMultiplier, baseMultiplier)
		}
		if want := baseMultiplier * 3.0; !approxEq(tokenMultiplier, want) {
			t.Fatalf("token multiplier should include peak factor: got %v, want %v", tokenMultiplier, want)
		}
	})

	t.Run("off-peak leaves both multipliers at base", func(t *testing.T) {
		now := at(20, 0)
		tokenMultiplier, imageMultiplier := completion.ComputePeakAwareMultipliers(keySnapshot, baseMultiplier, now)
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("image multiplier: got %v, want %v", imageMultiplier, baseMultiplier)
		}
		if !approxEq(tokenMultiplier, baseMultiplier) {
			t.Fatalf("token multiplier should equal base off-peak: got %v, want %v", tokenMultiplier, baseMultiplier)
		}
	})

	t.Run("nil api key degrades to base multipliers", func(t *testing.T) {
		now := at(15, 30)
		tokenMultiplier, imageMultiplier := completion.ComputePeakAwareMultipliers(nil, baseMultiplier, now)
		if !approxEq(tokenMultiplier, baseMultiplier) {
			t.Fatalf("nil group token multiplier: got %v, want %v", tokenMultiplier, baseMultiplier)
		}
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("nil group image multiplier: got %v, want %v", imageMultiplier, baseMultiplier)
		}
	})
}

// newPeakGroup constructs a completion group with the supplied peak schedule.
// Location remains unset so each caller chooses the timezone before evaluating
// these wall-clock start and end values.
func newPeakGroup(enabled bool, start, end string, mult float64) *completion.GroupSnapshot {
	return &completion.GroupSnapshot{
		PeakRateEnabled:    enabled,
		PeakStart:          start,
		PeakEnd:            end,
		PeakRateMultiplier: mult,
	}
}

// at returns a UTC instant on the peak fixture's date.
func at(hour, min int) time.Time {
	return time.Date(2026, 6, 29, hour, min, 0, 0, time.UTC)
}
