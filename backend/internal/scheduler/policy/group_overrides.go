package policy

// GroupAdvancedSchedulerOverrides contains sparse group overrides for advanced scheduling.
// Nil inherits gateway settings; pointers distinguish supplied false and zero values from missing fields.
type GroupAdvancedSchedulerOverrides struct {
	StickyWeightedEnabled       *bool    `json:"sticky_weighted_enabled,omitempty"`
	SubscriptionPriorityEnabled *bool    `json:"subscription_priority_enabled,omitempty"`
	EWMAErrorRateAlpha          *float64 `json:"ewma_error_rate_alpha,omitempty"`
	EWMATTFTAlpha               *float64 `json:"ewma_ttft_alpha,omitempty"`
	StickyEscapeEnabled         *bool    `json:"sticky_escape_enabled,omitempty"`
	StickyEscapeTTFTMs          *int     `json:"sticky_escape_ttft_ms,omitempty"`
	StickyEscapeErrorRate       *float64 `json:"sticky_escape_error_rate,omitempty"`
	LBTopK                      *int     `json:"lb_top_k,omitempty"`
	WeightPriority              *float64 `json:"weight_priority,omitempty"`
	WeightLoad                  *float64 `json:"weight_load,omitempty"`
	WeightQueue                 *float64 `json:"weight_queue,omitempty"`
	WeightErrorRate             *float64 `json:"weight_error_rate,omitempty"`
	WeightTTFT                  *float64 `json:"weight_ttft,omitempty"`
	WeightReset                 *float64 `json:"weight_reset,omitempty"`
	WeightQuotaHeadroom         *float64 `json:"weight_quota_headroom,omitempty"`
	WeightPreviousResponse      *float64 `json:"weight_previous_response,omitempty"`
	WeightSessionSticky         *float64 `json:"weight_session_sticky,omitempty"`
}
