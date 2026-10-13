package payment

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Checkout plan snapshots record subscription validity as a whole day count
// before issuance. These cases exercise billing/plan_validation.go conversion
// as used by checkout_draft.go, where week and month units become fixed day
// durations before the plan snapshot is saved.

// TestComputeValidityDaysSupportsSingularAndPluralUnits supplies day, week,
// and month spellings used by checkout plan snapshots. Billing conversion
// returns the expected whole day count for each supplied singular or plural
// unit.
func TestComputeValidityDaysSupportsSingularAndPluralUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // case diagnostic name
		days int    // supplied validity period count
		unit string // supplied validity unit spelling
		want int    // expected whole day count
	}{
		{name: "days", days: 1, unit: "days", want: 1},
		{name: "week", days: 1, unit: "week", want: 7},
		{name: "weeks", days: 2, unit: "weeks", want: 14},
		{name: "month", days: 1, unit: "month", want: 30},
		{name: "months", days: 1, unit: "months", want: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := billing.ComputeValidityDays(tt.days, tt.unit); got != tt.want {
				t.Fatalf("psComputeValidityDays(%d, %q) = %d, want %d", tt.days, tt.unit, got, tt.want)
			}
		})
	}
}
