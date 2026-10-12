package billing

// QuotaNotifyDimension contains committed usage and alert settings for one quota window.
type QuotaNotifyDimension struct {
	Name               string
	Enabled            bool
	Threshold          float64
	ThresholdType      string
	CurrentUsed, Limit float64
}

// BalanceThreshold converts percentages of positive total recharges; otherwise it returns the threshold unchanged.
func BalanceThreshold(threshold float64, kind string, recharged float64) float64 {
	if kind == "percentage" && recharged > 0 {
		return recharged * threshold / 100
	}
	return threshold
}

func CrossedDownward(oldValue, newValue, threshold float64) bool {
	return oldValue >= threshold && newValue < threshold
}

// EffectiveBalanceThreshold applies the global switch and user override, returning false for disabled or
// nonpositive thresholds.
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

func (d QuotaNotifyDimension) UsageThreshold() float64 {
	if d.Limit <= 0 {
		return 0
	}
	if d.ThresholdType == "percentage" {
		return d.Limit * (1 - d.Threshold/100)
	}
	return d.Limit - d.Threshold
}

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
