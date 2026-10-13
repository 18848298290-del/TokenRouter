package ops

import (
	"testing"
	"time"
)

// TestOpsCleanupPlan applies negative, zero, and positive retention days to a
// fixed reference time. It checks the skip decision, whole-table action, and
// calendar-day cutoff returned for those distinct policies.
func TestOpsCleanupPlan(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string // retention case display name
		days int    // configured retention day input
		// wantOK controls whether an action is expected. When true, wantTruncate
		// selects the whole-table assertion, and wantCutoff is compared only for
		// positive-retention deletion plans.
		wantOK       bool      // expected executable cleanup plan
		wantTruncate bool      // expected whole-table cleanup action
		wantCutoff   time.Time // expected calendar retention cutoff
	}{
		{name: "negative skips", days: -1, wantOK: false},
		{name: "zero truncates", days: 0, wantOK: true, wantTruncate: true},
		{name: "positive yields past cutoff", days: 7, wantOK: true, wantCutoff: now.AddDate(0, 0, -7)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cutoff, truncate, ok := opsCleanupPlan(now, tc.days)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if truncate != tc.wantTruncate {
				t.Fatalf("truncate = %v, want %v", truncate, tc.wantTruncate)
			}
			if !tc.wantTruncate && !cutoff.Equal(tc.wantCutoff) {
				t.Fatalf("cutoff = %v, want %v", cutoff, tc.wantCutoff)
			}
		})
	}
}
