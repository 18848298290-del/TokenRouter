package pricingcontract

import (
	"math"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
)

// TestPeakMultiplier_GatewayBillingSequence checks the shared completion multiplier calculation.
// The image rate uses the base multiplier.
// Peak pricing multiplies the token rate.
func TestPeakMultiplier_GatewayBillingSequence(t *testing.T) {
	const baseMultiplier = 0.8
	keySnapshot := &completion.KeySnapshot{Group: newPeakGroup(true, "14:00", "18:00", 3.0)}
	// Keep the fixture in UTC without changing the process-wide timezone.
	keySnapshot.Group.Location = time.UTC
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

func newPeakGroup(enabled bool, start, end string, mult float64) *completion.GroupSnapshot {
	return &completion.GroupSnapshot{
		PeakRateEnabled:    enabled,
		PeakStart:          start,
		PeakEnd:            end,
		PeakRateMultiplier: mult,
	}
}

func at(hour, min int) time.Time {
	return time.Date(2026, 6, 29, hour, min, 0, 0, time.UTC)
}
