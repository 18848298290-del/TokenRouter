package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetIntervalForContext selects prices around the shared 128000-token
// endpoint and an unbounded tier. The upper endpoint belongs to the first
// range, the next token selects the second, and zero has no match.
func TestGetIntervalForContext(t *testing.T) {
	p := &ModelPricingEntry{
		Intervals: []PricingInterval{
			{MinTokens: 0, MaxTokens: new(int(128000)), InputPrice: new(float64(1e-6))},
			{MinTokens: 128000, MaxTokens: nil, InputPrice: new(float64(2e-6))},
		},
	}

	tests := []struct {
		name   string // subtest display case label
		tokens int    // queried context-token count
		// wantNil selects the absence assertion; wantPrice is checked only after a
		// non-nil match. Together they distinguish missing ranges from priced
		// interval results.
		wantPrice *float64 // expected matched input amount
		wantNil   bool     // expected absent matching result
	}{
		{"first interval", 50000, new(float64(1e-6)), false},
		// The (min, max] range includes 128000 in the first interval.
		{"boundary: max of first (inclusive)", 128000, new(float64(1e-6)), false},
		// 128001 belongs to the interval whose lower endpoint is 128000.
		{"boundary: just above first max", 128001, new(float64(2e-6)), false},
		{"unbounded interval", 500000, new(float64(2e-6)), false},
		// Zero is outside the (0, max] range.
		{"zero tokens: no match", 0, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindMatchingInterval(p.Intervals, tt.tokens)
			if tt.wantNil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			// Allow decimal float64 representation tolerance while checking the selected
			// per-token amounts at context endpoints.
			require.InDelta(t, *tt.wantPrice, *result.InputPrice, 1e-12)
		})
	}
}

// TestGetIntervalForContext_NoMatch queries both endpoints and nearby values of
// one bounded interval. Only the included upper endpoint matches; lower,
// below-range, and above-range counts return nil.
func TestGetIntervalForContext_NoMatch(t *testing.T) {
	p := &ModelPricingEntry{
		Intervals: []PricingInterval{
			{MinTokens: 10000, MaxTokens: new(int(50000))},
		},
	}
	require.Nil(t, FindMatchingInterval(p.Intervals, 5000))     // Below the lower endpoint.
	require.Nil(t, FindMatchingInterval(p.Intervals, 10000))    // The lower endpoint is excluded.
	require.NotNil(t, FindMatchingInterval(p.Intervals, 50000)) // The upper endpoint is included.
	require.Nil(t, FindMatchingInterval(p.Intervals, 50001))    // Above the upper endpoint.
}

// TestGetIntervalForContext_Empty queries positive usage against an absent
// interval list. The lookup must return nil so callers can select their
// available default price.
func TestGetIntervalForContext_Empty(t *testing.T) {
	p := &ModelPricingEntry{Intervals: nil}
	require.Nil(t, FindMatchingInterval(p.Intervals, 1000))
}

// TestGetTierByLabel queries exact, differently cased, and absent size labels.
// Existing labels return their programmed unit amounts, while an unconfigured
// label returns nil.
func TestGetTierByLabel(t *testing.T) {
	p := &ModelPricingEntry{
		Intervals: []PricingInterval{
			{TierLabel: "1K", PerRequestPrice: new(float64(0.04))},
			{TierLabel: "2K", PerRequestPrice: new(float64(0.08))},
			{TierLabel: "HD", PerRequestPrice: new(float64(0.12))},
		},
	}

	tests := []struct {
		name  string // subtest display case label
		label string // queried tier label text
		// wantNil selects the missing-tier assertion; want is read only for matched
		// tiers. This lets the table keep absence separate from a numeric zero
		// amount.
		wantNil bool    // expected absent matching result
		want    float64 // expected matched unit amount
	}{
		{"exact match", "1K", false, 0.04},
		{"case insensitive", "hd", false, 0.12},
		{"not found", "4K", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.GetTierByLabel(tt.label)
			if tt.wantNil {
				require.Nil(t, result)
				return
			}
			require.NotNil(t, result)
			// The expected flat unit amounts use decimal float64 literals; permit
			// representation tolerance in these label-price comparisons.
			require.InDelta(t, tt.want, *result.PerRequestPrice, 1e-12)
		})
	}
}

// TestGetTierByLabel_Empty requests a size label from an absent tier list. It
// must return nil, leaving the caller to handle the missing configured tier.
func TestGetTierByLabel_Empty(t *testing.T) {
	p := &ModelPricingEntry{Intervals: nil}
	require.Nil(t, p.GetTierByLabel("1K"))
}

// TestModelPricingEntryClone mutates copied model labels, interval labels, and
// nested time settings. Each asserted source value must remain unchanged after
// those mutations to the clone.
func TestModelPricingEntryClone(t *testing.T) {
	original := ModelPricingEntry{
		Models: []string{"a", "b"},
		Intervals: []PricingInterval{
			{MinTokens: 0, TierLabel: "tier1"},
		},
		TimePricing: &TimePricingConfig{
			Timezone:     "Asia/Shanghai",
			WeekdaysOnly: true,
			Periods: []TimePricingPeriod{{
				StartTime:  "09:00",
				EndTime:    "12:00",
				Multiplier: 2,
			}},
		},
	}

	cloned := original.Clone()

	// Change cloned labels and time settings before checking the source, so these
	// assertions expose accidentally shared mutable storage in those copied
	// fields.
	cloned.Models[0] = "hacked"
	require.Equal(t, "a", original.Models[0])

	cloned.Intervals[0].TierLabel = "hacked"
	require.Equal(t, "tier1", original.Intervals[0].TierLabel)

	cloned.TimePricing.Timezone = "America/New_York"
	cloned.TimePricing.WeekdaysOnly = false
	cloned.TimePricing.Periods[0].StartTime = "10:00"
	cloned.TimePricing.Periods[0].Multiplier = 3
	require.Equal(t, "Asia/Shanghai", original.TimePricing.Timezone)
	require.True(t, original.TimePricing.WeekdaysOnly)
	require.Equal(t, "09:00", original.TimePricing.Periods[0].StartTime)
	require.Equal(t, 2.0, original.TimePricing.Periods[0].Multiplier)
}

// TestBillingModeIsValid checks token, per-request, image, empty, and two
// unsupported mode strings. The listed supported values and empty default
// succeed; the unsupported strings fail.
func TestBillingModeIsValid(t *testing.T) {
	tests := []struct {
		name string      // subtest display case label
		mode BillingMode // billing mode being validated
		want bool        // expected mode acceptance flag
	}{
		{"token", BillingModeToken, true},
		{"per_request", BillingModePerRequest, true},
		{"image", BillingModeImage, true},
		{"empty", BillingMode(""), true},
		{"unknown", BillingMode("unknown"), false},
		{"random", BillingMode("xyz"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.mode.IsValid())
		})
	}
}

// TestModelPricingEntryClone_EdgeCases clones nil model and interval slices
// plus a present empty model slice. Nil slices stay nil, and the empty model
// slice remains present.
func TestModelPricingEntryClone_EdgeCases(t *testing.T) {
	t.Run("nil models", func(t *testing.T) {
		original := ModelPricingEntry{Models: nil}
		cloned := original.Clone()
		require.Nil(t, cloned.Models)
	})

	t.Run("nil intervals", func(t *testing.T) {
		original := ModelPricingEntry{Intervals: nil}
		cloned := original.Clone()
		require.Nil(t, cloned.Intervals)
	})

	t.Run("empty models", func(t *testing.T) {
		original := ModelPricingEntry{Models: []string{}}
		cloned := original.Clone()
		require.NotNil(t, cloned.Models)
		require.Empty(t, cloned.Models)
	})
}

// TestValidateIntervals_Empty submits nil and present empty token interval
// lists. Both must validate successfully because no supplied interval can
// contain an invalid field or overlap.
func TestValidateIntervals_Empty(t *testing.T) {
	require.NoError(t, ValidateIntervals(nil, BillingModeToken))
	require.NoError(t, ValidateIntervals([]PricingInterval{}, BillingModeToken))
}

// TestValidateIntervals_ValidIntervals submits bounded, adjoining, gapped,
// unbounded, and unsorted valid ranges. Validation must accept every case,
// including input whose minimum-token order differs from its validation order.
func TestValidateIntervals_ValidIntervals(t *testing.T) {
	tests := []struct {
		name      string            // subtest display case label
		intervals []PricingInterval // supplied interval validation input
	}{
		{
			name: "single bounded interval",
			intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: new(int(128000)), InputPrice: new(float64(1e-6))},
			},
		},
		{
			name: "two intervals with gap",
			intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: new(int(100000)), InputPrice: new(float64(1e-6))},
				{MinTokens: 128000, MaxTokens: nil, InputPrice: new(float64(2e-6))},
			},
		},
		{
			name: "two contiguous intervals",
			intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: new(int(128000)), InputPrice: new(float64(1e-6))},
				{MinTokens: 128000, MaxTokens: nil, InputPrice: new(float64(2e-6))},
			},
		},
		{
			name: "unsorted input (auto-sorted by validator)",
			intervals: []PricingInterval{
				{MinTokens: 128000, MaxTokens: nil, InputPrice: new(float64(2e-6))},
				{MinTokens: 0, MaxTokens: new(int(128000)), InputPrice: new(float64(1e-6))},
			},
		},
		{
			name: "single unbounded interval",
			intervals: []PricingInterval{
				{MinTokens: 0, MaxTokens: nil, InputPrice: new(float64(1e-6))},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, ValidateIntervals(tt.intervals, BillingModeToken))
		})
	}
}

// TestValidateIntervals_NegativeMinTokens supplies a negative lower endpoint
// with a valid upper endpoint. Validation must report min_tokens and its
// nonnegative requirement in the error.
func TestValidateIntervals_NegativeMinTokens(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: -1, MaxTokens: new(int(100)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_tokens")
	require.Contains(t, err.Error(), ">= 0")
}

// TestValidateIntervals_MaxTokensZero supplies zero as a present upper
// endpoint. Validation must reject that endpoint and identify max_tokens with
// its positive-value requirement.
func TestValidateIntervals_MaxTokensZero(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: new(int(0)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tokens")
	require.Contains(t, err.Error(), "> 0")
}

// TestValidateIntervals_MaxLessThanMin supplies a positive maximum below the
// minimum. Validation must identify max_tokens and report that its value must
// exceed the lower endpoint.
func TestValidateIntervals_MaxLessThanMin(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 100, MaxTokens: new(int(50)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tokens")
	require.Contains(t, err.Error(), "> min_tokens")
}

// TestValidateIntervals_MaxEqualsMin submits equal positive endpoints for a
// token interval. Validation must reject the empty range and name the
// requirement that max_tokens exceed min_tokens.
func TestValidateIntervals_MaxEqualsMin(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 100, MaxTokens: new(int(100)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tokens")
	require.Contains(t, err.Error(), "> min_tokens")
}

// TestValidateIntervals_NegativePrice supplies a negative input amount on an
// otherwise valid range. Validation must reject input_price and include its
// nonnegative requirement in the error.
func TestValidateIntervals_NegativePrice(t *testing.T) {
	negPrice := -0.01
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: new(int(100)), InputPrice: &negPrice},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_price")
	require.Contains(t, err.Error(), ">= 0")
}

// TestValidateIntervals_OverlappingIntervals supplies two token ranges with
// shared interior values. Validation must reject those ranges with an overlap
// error so usage has one selected interval.
func TestValidateIntervals_OverlappingIntervals(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: new(int(200)), InputPrice: new(float64(1e-6))},
		{MinTokens: 100, MaxTokens: new(int(300)), InputPrice: new(float64(2e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "overlap")
}

// TestValidateIntervals_UnboundedNotLast places an unbounded token range before
// a later bounded range. Validation must reject the ordering and identify that
// the unbounded interval must be last.
func TestValidateIntervals_UnboundedNotLast(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: nil, InputPrice: new(float64(1e-6))},
		{MinTokens: 128000, MaxTokens: new(int(256000)), InputPrice: new(float64(2e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unbounded")
	require.Contains(t, err.Error(), "last")
}

// TestValidateIntervals_ImageModeAllowsMultipleUnboundedTiers submits three
// labeled tiers with identical unbounded ranges. Image and per-request modes
// must accept them because those labels select distinct unit prices.
func TestValidateIntervals_ImageModeAllowsMultipleUnboundedTiers(t *testing.T) {
	// Labeled image/request tiers may each have min=0 and max=nil.
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: nil, TierLabel: "1K", PerRequestPrice: new(float64(0.04))},
		{MinTokens: 0, MaxTokens: nil, TierLabel: "2K", PerRequestPrice: new(float64(0.06))},
		{MinTokens: 0, MaxTokens: nil, TierLabel: "4K", PerRequestPrice: new(float64(0.08))},
	}
	require.NoError(t, ValidateIntervals(intervals, BillingModeImage))
	require.NoError(t, ValidateIntervals(intervals, BillingModePerRequest))
}

// TestValidateIntervals_ImageModeStillRejectsNegativePrice gives a labeled
// image tier a negative unit amount. Image-mode validation must reject the
// amount with the nonnegative-price error.
func TestValidateIntervals_ImageModeStillRejectsNegativePrice(t *testing.T) {
	// Image tiers still require nonnegative prices.
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: nil, TierLabel: "1K", PerRequestPrice: new(float64(-1))},
	}
	err := ValidateIntervals(intervals, BillingModeImage)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be >= 0")
}

// TestValidateIntervals_ImageModeStillRejectsBadMaxTokens gives a labeled image
// tier an upper endpoint below its minimum. Image-mode validation must still
// reject the invalid endpoint relationship.
func TestValidateIntervals_ImageModeStillRejectsBadMaxTokens(t *testing.T) {
	// Image tiers still require max_tokens greater than min_tokens.
	intervals := []PricingInterval{
		{MinTokens: 100, MaxTokens: new(int(50)), TierLabel: "1K", PerRequestPrice: new(float64(0.04))},
	}
	err := ValidateIntervals(intervals, BillingModeImage)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be > min_tokens")
}
