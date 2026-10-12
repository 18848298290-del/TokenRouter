package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetIntervalForContext(t *testing.T) {
	p := &ModelPricingEntry{
		Intervals: []PricingInterval{
			{MinTokens: 0, MaxTokens: new(int(128000)), InputPrice: new(float64(1e-6))},
			{MinTokens: 128000, MaxTokens: nil, InputPrice: new(float64(2e-6))},
		},
	}

	tests := []struct {
		name      string
		tokens    int
		wantPrice *float64
		wantNil   bool
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
			require.InDelta(t, *tt.wantPrice, *result.InputPrice, 1e-12)
		})
	}
}

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

func TestGetIntervalForContext_Empty(t *testing.T) {
	p := &ModelPricingEntry{Intervals: nil}
	require.Nil(t, FindMatchingInterval(p.Intervals, 1000))
}

func TestGetTierByLabel(t *testing.T) {
	p := &ModelPricingEntry{
		Intervals: []PricingInterval{
			{TierLabel: "1K", PerRequestPrice: new(float64(0.04))},
			{TierLabel: "2K", PerRequestPrice: new(float64(0.08))},
			{TierLabel: "HD", PerRequestPrice: new(float64(0.12))},
		},
	}

	tests := []struct {
		name    string
		label   string
		wantNil bool
		want    float64
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
			require.InDelta(t, tt.want, *result.PerRequestPrice, 1e-12)
		})
	}
}

func TestGetTierByLabel_Empty(t *testing.T) {
	p := &ModelPricingEntry{Intervals: nil}
	require.Nil(t, p.GetTierByLabel("1K"))
}

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

	// Mutating the copy must leave the source slices and time configuration unchanged.
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

func TestBillingModeIsValid(t *testing.T) {
	tests := []struct {
		name string
		mode BillingMode
		want bool
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

func TestValidateIntervals_Empty(t *testing.T) {
	require.NoError(t, ValidateIntervals(nil, BillingModeToken))
	require.NoError(t, ValidateIntervals([]PricingInterval{}, BillingModeToken))
}

func TestValidateIntervals_ValidIntervals(t *testing.T) {
	tests := []struct {
		name      string
		intervals []PricingInterval
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

func TestValidateIntervals_NegativeMinTokens(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: -1, MaxTokens: new(int(100)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_tokens")
	require.Contains(t, err.Error(), ">= 0")
}

func TestValidateIntervals_MaxTokensZero(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: new(int(0)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tokens")
	require.Contains(t, err.Error(), "> 0")
}

func TestValidateIntervals_MaxLessThanMin(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 100, MaxTokens: new(int(50)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tokens")
	require.Contains(t, err.Error(), "> min_tokens")
}

func TestValidateIntervals_MaxEqualsMin(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 100, MaxTokens: new(int(100)), InputPrice: new(float64(1e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_tokens")
	require.Contains(t, err.Error(), "> min_tokens")
}

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

func TestValidateIntervals_OverlappingIntervals(t *testing.T) {
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: new(int(200)), InputPrice: new(float64(1e-6))},
		{MinTokens: 100, MaxTokens: new(int(300)), InputPrice: new(float64(2e-6))},
	}
	err := ValidateIntervals(intervals, BillingModeToken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "overlap")
}

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

func TestValidateIntervals_ImageModeStillRejectsNegativePrice(t *testing.T) {
	// Image tiers still require nonnegative prices.
	intervals := []PricingInterval{
		{MinTokens: 0, MaxTokens: nil, TierLabel: "1K", PerRequestPrice: new(float64(-1))},
	}
	err := ValidateIntervals(intervals, BillingModeImage)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be >= 0")
}

func TestValidateIntervals_ImageModeStillRejectsBadMaxTokens(t *testing.T) {
	// Image tiers still require max_tokens greater than min_tokens.
	intervals := []PricingInterval{
		{MinTokens: 100, MaxTokens: new(int(50)), TierLabel: "1K", PerRequestPrice: new(float64(0.04))},
	}
	err := ValidateIntervals(intervals, BillingModeImage)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be > min_tokens")
}
