package billing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCrossedDownward_CrossesBelow deducts from above a positive balance
// threshold to below it. It checks a crossing is reported for that one balance
// transition.
func TestCrossedDownward_CrossesBelow(t *testing.T) {
	require.True(t, CrossedDownward(100, 5, 10))
}

// TestCrossedDownward_ExactlyAtThreshold ends a balance deduction exactly at
// its threshold. It checks no downward crossing is reported because the new
// balance has not fallen below.
func TestCrossedDownward_ExactlyAtThreshold(t *testing.T) {
	require.False(t, CrossedDownward(100, 10, 10))
}

// TestCrossedDownward_OldExactlyAtThreshold_NewBelow starts a deduction
// exactly at the balance threshold and ends below it. It checks that equality
// on the old side still allows a crossing.
func TestCrossedDownward_OldExactlyAtThreshold_NewBelow(t *testing.T) {
	require.True(t, CrossedDownward(10, 5, 10))
}

// TestCrossedDownward_AlreadyBelow starts and ends below the balance
// threshold. It checks that a later deduction in this state does not report
// another crossing.
func TestCrossedDownward_AlreadyBelow(t *testing.T) {
	require.False(t, CrossedDownward(5, 3, 10))
}

// TestCrossedDownward_BothAbove keeps both balance observations above the
// threshold. It checks that a deduction short of the threshold does not report
// a crossing.
func TestCrossedDownward_BothAbove(t *testing.T) {
	require.False(t, CrossedDownward(100, 50, 10))
}

// TestCrossedDownward_ZeroThreshold keeps the new balance positive or zero
// against a zero threshold. It checks that those transitions do not report a
// downward crossing.
func TestCrossedDownward_ZeroThreshold(t *testing.T) {
	require.False(t, CrossedDownward(10, 5, 0))
	require.False(t, CrossedDownward(0, 0, 0))
}

// TestCrossedDownward_ZeroThreshold_NegativeNew deducts a positive balance
// into debt against a zero threshold. It checks the predicate reports the
// transition below zero for these observations.
func TestCrossedDownward_ZeroThreshold_NegativeNew(t *testing.T) {
	require.True(t, CrossedDownward(5, -1, 0))
}

// TestCrossedDownward_NegativeValues starts and ends with debt while the
// threshold is positive. It checks this already-below transition does not
// create a new crossing.
func TestCrossedDownward_NegativeValues(t *testing.T) {
	require.False(t, CrossedDownward(-5, -10, 10))
}

// TestCrossedDownward_LargeDecrement deducts almost an entire balance in one
// change. It checks that crossing detection depends on the threshold
// transition even for a large decrement.
func TestCrossedDownward_LargeDecrement(t *testing.T) {
	require.True(t, CrossedDownward(1000, 0.5, 100))
}

// TestCrossedDownward_SmallDecrement_NoCrossing applies a small decrement far
// above the balance threshold. It checks that size alone does not make a
// non-crossing change report an alert.
func TestCrossedDownward_SmallDecrement_NoCrossing(t *testing.T) {
	require.False(t, CrossedDownward(100, 99.99, 10))
}

// TestResolveBalanceThreshold_Fixed uses fixed thresholds with positive and
// absent recharge totals. It checks the raw amount is retained, including a
// configured zero amount.
func TestResolveBalanceThreshold_Fixed(t *testing.T) {
	require.Equal(t, 10.0, BalanceThreshold(10, thresholdTypeFixed, 1000))
	require.Equal(t, 10.0, BalanceThreshold(10, thresholdTypeFixed, 0))
	require.Equal(t, 0.0, BalanceThreshold(0, thresholdTypeFixed, 1000))
}

// TestResolveBalanceThreshold_Percentage uses two percentage thresholds with
// positive recharge totals. It checks that both percentages convert to the
// expected balance amount using their respective totals.
func TestResolveBalanceThreshold_Percentage(t *testing.T) {
	require.Equal(t, 100.0, BalanceThreshold(10, thresholdTypePercentage, 1000))
	require.Equal(t, 100.0, BalanceThreshold(50, thresholdTypePercentage, 200))
}

// TestResolveBalanceThreshold_PercentageZeroRecharged uses a percentage
// threshold with no cumulative recharge total. It checks the supplied number
// is retained as a fixed amount in that case.
func TestResolveBalanceThreshold_PercentageZeroRecharged(t *testing.T) {
	require.Equal(t, 10.0, BalanceThreshold(10, thresholdTypePercentage, 0))
}

// TestResolveBalanceThreshold_EmptyType uses an empty threshold-kind string
// with positive recharges. It checks that the value is treated as a fixed
// amount for this input.
func TestResolveBalanceThreshold_EmptyType(t *testing.T) {
	require.Equal(t, 10.0, BalanceThreshold(10, "", 1000))
}

// TestResolvedThreshold_FixedNormal configures a remaining amount smaller than
// a finite quota limit. It checks the equivalent used-quota trigger subtracts
// that remaining amount from the limit.
func TestResolvedThreshold_FixedNormal(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 400, ThresholdType: thresholdTypeFixed, Limit: 1000}
	require.Equal(t, 600.0, d.UsageThreshold())
}

// TestResolvedThreshold_FixedThresholdExceedsLimit configures a remaining
// amount larger than the quota limit. It checks the computed used trigger
// remains negative for the crossing caller to suppress.
func TestResolvedThreshold_FixedThresholdExceedsLimit(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 1200, ThresholdType: thresholdTypeFixed, Limit: 1000}
	require.Equal(t, -200.0, d.UsageThreshold())
}

// TestResolvedThreshold_FixedThresholdEqualsLimit configures remaining quota
// equal to its limit. It checks the computed used trigger is zero, retaining
// that value for later crossing eligibility checks.
func TestResolvedThreshold_FixedThresholdEqualsLimit(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 1000, ThresholdType: thresholdTypeFixed, Limit: 1000}
	require.Equal(t, 0.0, d.UsageThreshold())
}

// TestResolvedThreshold_PercentageNormal configures thirty percent remaining
// on a finite quota. It checks the computed usage trigger equals seventy
// percent of the limit within the arithmetic tolerance.
func TestResolvedThreshold_PercentageNormal(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 30, ThresholdType: thresholdTypePercentage, Limit: 1000}
	// Percentage-to-amount arithmetic uses binary floating point; this tolerance
	// allows rounding noise while keeping the asserted quota trigger near its
	// expected amount.
	require.InDelta(t, 700.0, d.UsageThreshold(), 0.001)
}

// TestResolvedThreshold_PercentageZeroPercent configures a zero-percent
// remaining threshold. It checks the computed used trigger equals the limit,
// independently of the crossing method's configured-threshold enablement rule.
func TestResolvedThreshold_PercentageZeroPercent(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 0, ThresholdType: thresholdTypePercentage, Limit: 1000}
	// Percentage-to-amount arithmetic uses binary floating point; this tolerance
	// allows rounding noise while keeping the asserted quota trigger near its
	// expected amount.
	require.InDelta(t, 1000.0, d.UsageThreshold(), 0.001)
}

// TestResolvedThreshold_PercentageHundredPercent configures a hundred-percent
// remaining threshold. It checks the computed used trigger is zero so
// threshold conversion retains its endpoint result.
func TestResolvedThreshold_PercentageHundredPercent(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 100, ThresholdType: thresholdTypePercentage, Limit: 1000}
	// Percentage-to-amount arithmetic uses binary floating point; this tolerance
	// allows rounding noise while keeping the asserted quota trigger near its
	// expected amount.
	require.InDelta(t, 0.0, d.UsageThreshold(), 0.001)
}

// TestResolvedThreshold_PercentageOverHundred configures a remaining
// percentage above a hundred percent. It checks the computed trigger is
// negative for the caller's nonpositive-trigger handling.
func TestResolvedThreshold_PercentageOverHundred(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 150, ThresholdType: thresholdTypePercentage, Limit: 1000}
	require.Less(t, d.UsageThreshold(), 0.0)
}

// TestResolvedThreshold_ZeroLimit passes a zero quota limit with a positive
// fixed threshold. It checks zero usage trigger, representing the absence of a
// finite quota ceiling.
func TestResolvedThreshold_ZeroLimit(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 100, ThresholdType: thresholdTypeFixed, Limit: 0}
	require.Equal(t, 0.0, d.UsageThreshold())
}

// TestResolvedThreshold_NegativeLimit passes a negative quota limit with a
// positive fixed threshold. It checks zero usage trigger so nonpositive limits
// share the disabled-limit result.
func TestResolvedThreshold_NegativeLimit(t *testing.T) {
	d := QuotaNotifyDimension{Threshold: 100, ThresholdType: thresholdTypeFixed, Limit: -10}
	require.Equal(t, 0.0, d.UsageThreshold())
}
