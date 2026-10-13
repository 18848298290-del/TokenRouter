package policy

// A group configuration snapshot contains pointers so it can distinguish
// inheritance from explicit values. Copying the struct alone would retain
// those shared addresses; cloning allocates supplied values again so request
// or administration changes cannot modify the copied configuration through
// them.

// CloneGroupAdvancedSchedulerOverrides copies every supplied pointer into
// newly allocated storage. Nil pointers remain nil, retaining field
// inheritance; callers can modify pointed-to values in the result
// independently from the input overrides.
func CloneGroupAdvancedSchedulerOverrides(overrides GroupAdvancedSchedulerOverrides) GroupAdvancedSchedulerOverrides {
	cloneBool := func(value *bool) *bool {
		if value == nil {
			return nil
		}
		cloned := *value
		return &cloned
	}
	cloneInt := func(value *int) *int {
		if value == nil {
			return nil
		}
		cloned := *value
		return &cloned
	}
	cloneFloat := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		cloned := *value
		return &cloned
	}
	return GroupAdvancedSchedulerOverrides{
		StickyWeightedEnabled:       cloneBool(overrides.StickyWeightedEnabled),
		SubscriptionPriorityEnabled: cloneBool(overrides.SubscriptionPriorityEnabled),
		EWMAErrorRateAlpha:          cloneFloat(overrides.EWMAErrorRateAlpha),
		EWMATTFTAlpha:               cloneFloat(overrides.EWMATTFTAlpha),
		StickyEscapeEnabled:         cloneBool(overrides.StickyEscapeEnabled),
		StickyEscapeTTFTMs:          cloneInt(overrides.StickyEscapeTTFTMs),
		StickyEscapeErrorRate:       cloneFloat(overrides.StickyEscapeErrorRate),
		LBTopK:                      cloneInt(overrides.LBTopK),
		WeightPriority:              cloneFloat(overrides.WeightPriority),
		WeightLoad:                  cloneFloat(overrides.WeightLoad),
		WeightQueue:                 cloneFloat(overrides.WeightQueue),
		WeightErrorRate:             cloneFloat(overrides.WeightErrorRate),
		WeightTTFT:                  cloneFloat(overrides.WeightTTFT),
		WeightReset:                 cloneFloat(overrides.WeightReset),
		WeightQuotaHeadroom:         cloneFloat(overrides.WeightQuotaHeadroom),
		WeightPreviousResponse:      cloneFloat(overrides.WeightPreviousResponse),
		WeightSessionSticky:         cloneFloat(overrides.WeightSessionSticky),
	}
}
