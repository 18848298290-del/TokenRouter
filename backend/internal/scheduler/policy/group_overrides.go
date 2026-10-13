package policy

// Group scheduler overrides are sparse values: omitting a field inherits the
// effective global setting, while a supplied false or zero remains meaningful.
// Pointer fields preserve that distinction in stored JSON and when request
// parameters are resolved.

// GroupAdvancedSchedulerOverrides represents optional group values for
// advanced selection and feedback. CloneGroupAdvancedSchedulerOverrides
// allocates each supplied pointer when a caller needs values that can be
// changed independently.
type GroupAdvancedSchedulerOverrides struct {
	// StickyWeightedEnabled and SubscriptionPriorityEnabled override their
	// inherited switches whenever supplied. Pointer presence retains a
	// requested false value when ResolveEffective builds the request
	// parameters.
	// weighted affinity switch
	StickyWeightedEnabled *bool `json:"sticky_weighted_enabled,omitempty"`
	// subscription pool preference
	SubscriptionPriorityEnabled *bool `json:"subscription_priority_enabled,omitempty"`
	// EWMAErrorRateAlpha and EWMATTFTAlpha override independently. Nil
	// inherits the corresponding global coefficient; validation accepts
	// positive finite coefficients through one for the new sample weight.
	// error sample coefficient
	EWMAErrorRateAlpha *float64 `json:"ewma_error_rate_alpha,omitempty"`
	// TTFT sample coefficient
	EWMATTFTAlpha *float64 `json:"ewma_ttft_alpha,omitempty"`
	// StickyEscapeEnabled inherits when nil; a supplied false disables the
	// inherited escape switch when ResolveEffective builds request
	// settings.
	//
	// health escape switch
	StickyEscapeEnabled *bool `json:"sticky_escape_enabled,omitempty"`
	// StickyEscapeTTFTMs and StickyEscapeErrorRate override the
	// corresponding inherited thresholds independently. Nil preserves that
	// threshold, while supplied values are normalized when
	// ResolveEffective builds request settings.
	// milliseconds
	StickyEscapeTTFTMs *int `json:"sticky_escape_ttft_ms,omitempty"`
	// error fraction threshold
	StickyEscapeErrorRate *float64 `json:"sticky_escape_error_rate,omitempty"`
	// LBTopK inherits when nil; ResolveEffective applies a supplied
	// positive count and otherwise retains the effective global candidate
	// limit.
	// sampled candidate count
	LBTopK *int `json:"lb_top_k,omitempty"`
	// WeightPriority, WeightLoad, WeightQueue, WeightErrorRate,
	// WeightTTFT, WeightReset, WeightQuotaHeadroom,
	// WeightPreviousResponse, and WeightSessionSticky replace their
	// individual global coefficients when non-nil. Explicit zero disables
	// that contribution, and ValidateEffectiveWeights checks the merged
	// sums before they are used.
	// global priority coefficient
	WeightPriority *float64 `json:"weight_priority,omitempty"`
	// provider load coefficient
	WeightLoad *float64 `json:"weight_load,omitempty"`
	// waiting count coefficient
	WeightQueue *float64 `json:"weight_queue,omitempty"`
	// feedback health coefficient
	WeightErrorRate *float64 `json:"weight_error_rate,omitempty"`
	// token latency coefficient
	WeightTTFT *float64 `json:"weight_ttft,omitempty"`
	// window reset coefficient
	WeightReset *float64 `json:"weight_reset,omitempty"`
	// quota headroom coefficient
	WeightQuotaHeadroom *float64 `json:"weight_quota_headroom,omitempty"`
	// previous response bonus
	WeightPreviousResponse *float64 `json:"weight_previous_response,omitempty"`
	// session provider bonus
	WeightSessionSticky *float64 `json:"weight_session_sticky,omitempty"`
}
