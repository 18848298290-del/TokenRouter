package billing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCrossedDownward_CrossesBelow(t *testing.T) {
	// oldBalance > threshold, newBalance < threshold → true
	require.True(t, CrossedDownward(100, 5, 10))
}

func TestCrossedDownward_ExactlyAtThreshold(t *testing.T) {
	// oldBalance > threshold, newBalance == threshold → false (not below)
	require.False(t, CrossedDownward(100, 10, 10))
}

func TestCrossedDownward_OldExactlyAtThreshold_NewBelow(t *testing.T) {
	// oldBalance == threshold, newBalance < threshold → true
	// (at-or-above → below counts as a crossing)
	require.True(t, CrossedDownward(10, 5, 10))
}

func TestCrossedDownward_AlreadyBelow(t *testing.T) {
	// oldBalance < threshold → false (already below, no new crossing)
	require.False(t, CrossedDownward(5, 3, 10))
}

func TestCrossedDownward_BothAbove(t *testing.T) {
	// oldBalance > threshold, newBalance > threshold → false (no crossing)
	require.False(t, CrossedDownward(100, 50, 10))
}

func TestCrossedDownward_ZeroThreshold(t *testing.T) {
	// A zero threshold requires a transition from a nonnegative to a negative balance.
	require.False(t, CrossedDownward(10, 5, 0))
	require.False(t, CrossedDownward(0, 0, 0))
}

func TestCrossedDownward_ZeroThreshold_NegativeNew(t *testing.T) {
	// Edge case: newBalance goes negative with threshold=0.
	require.True(t, CrossedDownward(5, -1, 0))
}

func TestCrossedDownward_NegativeValues(t *testing.T) {
	// Both already negative, threshold is positive → no crossing (already below).
	require.False(t, CrossedDownward(-5, -10, 10))
}

func TestCrossedDownward_LargeDecrement(t *testing.T) {
	// A single large deduction crosses the threshold.
	require.True(t, CrossedDownward(1000, 0.5, 100))
}

func TestCrossedDownward_SmallDecrement_NoCrossing(t *testing.T) {
	// A tiny deduction stays above threshold.
	require.False(t, CrossedDownward(100, 99.99, 10))
}

func TestResolveBalanceThreshold_Fixed(t *testing.T) {
	// Fixed type always returns the raw threshold regardless of totalRecharged.
	require.Equal(t, 10.0, BalanceThreshold(10, thresholdTypeFixed, 1000))
	require.Equal(t, 10.0, BalanceThreshold(10, thresholdTypeFixed, 0))
	require.Equal(t, 0.0, BalanceThreshold(0, thresholdTypeFixed, 1000))
}

func TestResolveBalanceThreshold_Percentage(t *testing.T) {
	// 10% of 1000 = 100
	require.Equal(t, 100.0, BalanceThreshold(10, thresholdTypePercentage, 1000))
	// 50% of 200 = 100
	require.Equal(t, 100.0, BalanceThreshold(50, thresholdTypePercentage, 200))
}

func TestResolveBalanceThreshold_PercentageZeroRecharged(t *testing.T) {
	// When totalRecharged is 0, percentage falls through to raw threshold
	// and is treated as a fixed amount.
	require.Equal(t, 10.0, BalanceThreshold(10, thresholdTypePercentage, 0))
}

func TestResolveBalanceThreshold_EmptyType(t *testing.T) {
	// Empty type is treated as fixed (not percentage).
	require.Equal(t, 10.0, BalanceThreshold(10, "", 1000))
}

func TestResolvedThreshold_FixedNormal(t *testing.T) {
	// threshold=400 remaining, limit=1000 → usage trigger at 600
	d := QuotaNotifyDimension{Threshold: 400, ThresholdType: thresholdTypeFixed, Limit: 1000}
	require.Equal(t, 600.0, d.UsageThreshold())
}

func TestResolvedThreshold_FixedThresholdExceedsLimit(t *testing.T) {
	// threshold=1200, limit=1000 → returns negative, callers must skip
	d := QuotaNotifyDimension{Threshold: 1200, ThresholdType: thresholdTypeFixed, Limit: 1000}
	require.Equal(t, -200.0, d.UsageThreshold())
}

func TestResolvedThreshold_FixedThresholdEqualsLimit(t *testing.T) {
	// The computed threshold is zero; Crossing suppresses nonpositive usage thresholds.
	d := QuotaNotifyDimension{Threshold: 1000, ThresholdType: thresholdTypeFixed, Limit: 1000}
	require.Equal(t, 0.0, d.UsageThreshold())
}

func TestResolvedThreshold_PercentageNormal(t *testing.T) {
	// threshold=30%, limit=1000 → usage trigger at 700 (remaining drops to 30%)
	d := QuotaNotifyDimension{Threshold: 30, ThresholdType: thresholdTypePercentage, Limit: 1000}
	require.InDelta(t, 700.0, d.UsageThreshold(), 0.001)
}

func TestResolvedThreshold_PercentageZeroPercent(t *testing.T) {
	// The computed usage threshold is the limit; Crossing separately rejects a zero configured threshold.
	d := QuotaNotifyDimension{Threshold: 0, ThresholdType: thresholdTypePercentage, Limit: 1000}
	require.InDelta(t, 1000.0, d.UsageThreshold(), 0.001)
}

func TestResolvedThreshold_PercentageHundredPercent(t *testing.T) {
	// A 100% remaining threshold computes to zero usage; Crossing suppresses it.
	d := QuotaNotifyDimension{Threshold: 100, ThresholdType: thresholdTypePercentage, Limit: 1000}
	require.InDelta(t, 0.0, d.UsageThreshold(), 0.001)
}

func TestResolvedThreshold_PercentageOverHundred(t *testing.T) {
	// threshold=150%, limit=1000 → returns negative (never triggers; callers skip)
	d := QuotaNotifyDimension{Threshold: 150, ThresholdType: thresholdTypePercentage, Limit: 1000}
	require.Less(t, d.UsageThreshold(), 0.0)
}

func TestResolvedThreshold_ZeroLimit(t *testing.T) {
	// Unlimited quotas have no usage threshold.
	d := QuotaNotifyDimension{Threshold: 100, ThresholdType: thresholdTypeFixed, Limit: 0}
	require.Equal(t, 0.0, d.UsageThreshold())
}

func TestResolvedThreshold_NegativeLimit(t *testing.T) {
	// Negative limit treated as 0
	d := QuotaNotifyDimension{Threshold: 100, ThresholdType: thresholdTypeFixed, Limit: -10}
	require.Equal(t, 0.0, d.UsageThreshold())
}
