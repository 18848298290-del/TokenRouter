package billing

// Balance alerts watch funds fall below a configured amount, while provider
// quota alerts watch used quota reach the equivalent remaining-quota trigger.
// Equality belongs to the old side for balance and the new side for quota, so
// repeated changes already past the trigger do not produce another crossing.
//
// Quota usage is observed after the increment, and subtracting its cost
// reconstructs the prior value. Percentage quota thresholds are based on the
// limit; percentage balance thresholds use positive cumulative recharges and
// otherwise retain their number as a fixed amount. Global enablement and a
// user override determine whether balance checking proceeds.

// QuotaNotifyDimension combines committed window usage with its provider alert
// configuration.
type QuotaNotifyDimension struct {
	Name    string // provider quota window name
	Enabled bool   // dimension alert enable switch
	// Threshold and ThresholdType describe remaining quota together: percentage
	// values use Limit as their base, and every other kind uses Threshold as a
	// fixed remaining amount.
	// Threshold is the desired remaining quota amount or percentage. Crossing
	// rejects nonpositive values even when UsageThreshold could produce a
	// positive used-quota trigger.
	Threshold     float64 // configured remaining quota threshold
	ThresholdType string  // fixed amount or percentage
	// CurrentUsed and Limit use the same quota units for this window. CurrentUsed
	// contains the committed increment; Crossing reconstructs prior usage by
	// subtracting the supplied cost.
	// CurrentUsed: committed quota usage; Limit: configured quota ceiling.
	CurrentUsed, Limit float64
}

// BalanceThreshold converts a percentage to a balance amount when total
// recharges are positive. Other kinds and a nonpositive recharge total retain
// the supplied threshold as a fixed amount; the caller decides whether that
// amount enables alerts.
func BalanceThreshold(threshold float64, kind string, recharged float64) float64 {
	if kind == "percentage" && recharged > 0 {
		// Balance crossing compares amounts, so whole-percent settings are converted
		// using the user's cumulative recharge total before that comparison.
		return recharged * threshold / 100
	}
	return threshold
}

// CrossedDownward reports whether oldValue is at or above threshold and
// newValue is below it. Equality on the old side permits a crossing, while a
// change ending exactly at the threshold remains outside the event. For a
// threshold of ten, ten to five crosses and twenty to ten does not.
//
// The observations describe one change and use the threshold's units. Later
// deductions already below the trigger return false. This predicate stores no
// delivery history; notification callers handle recipient deduplication after
// selecting a crossing.
func CrossedDownward(oldValue, newValue, threshold float64) bool {
	return oldValue >= threshold && newValue < threshold
}

// EffectiveBalanceThreshold returns the enabled balance alert amount after
// applying the global switch and user override. A disabled global switch
// returns false even with a user threshold. A supplied user amount replaces
// the global amount, including zero or a negative value that disables this
// check.
//
// Positive percentage thresholds use cumulative recharges when those recharges
// are positive. Otherwise the same number acts as a fixed amount. The
// helper returns false when the resulting amount is at or below zero before
// the notification caller evaluates a downward balance crossing.
func EffectiveBalanceThreshold(globalEnabled bool, globalThreshold float64, userThreshold *float64, kind string, recharged float64) (float64, bool) {
	if !globalEnabled {
		return 0, false
	}
	threshold := globalThreshold
	if userThreshold != nil {
		threshold = *userThreshold
	}
	if threshold <= 0 {
		return 0, false
	}
	effective := BalanceThreshold(threshold, kind, recharged)
	if effective <= 0 {
		return 0, false
	}
	return effective, true
}

// UsageThreshold converts a remaining-quota alert threshold into used-quota
// units. A nonpositive limit returns zero; percentages use the limit as their
// base, while fixed amounts are subtracted directly and may yield a
// nonpositive result.
func (d QuotaNotifyDimension) UsageThreshold() float64 {
	if d.Limit <= 0 {
		return 0
	}
	if d.ThresholdType == "percentage" {
		// Quota alert callers compare increasing usage, so remaining percentages are
		// inverted before evaluating whether the increment reaches its trigger.
		return d.Limit * (1 - d.Threshold/100)
	}
	return d.Limit - d.Threshold
}

// Crossing returns the used-quota trigger and whether this increment reaches
// it from below. CurrentUsed represents committed usage after cost was
// applied; subtracting cost reconstructs the prior observation. The
// notification caller supplies the cost in the same units as CurrentUsed and
// Limit.
//
// Disabled dimensions and nonpositive configured or computed thresholds
// suppress the event. Equality at the new usage value counts as reaching the
// trigger, while prior usage already at the trigger suppresses another event
// for this increment.
func (d QuotaNotifyDimension) Crossing(cost float64) (float64, bool) {
	if !d.Enabled || d.Threshold <= 0 {
		return 0, false
	}
	threshold := d.UsageThreshold()
	if threshold <= 0 {
		return 0, false
	}
	return threshold, d.CurrentUsed-cost < threshold && d.CurrentUsed >= threshold
}
