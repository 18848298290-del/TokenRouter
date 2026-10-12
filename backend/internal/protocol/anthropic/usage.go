package anthropic

// ClaudeUsageWindow is one window returned by Anthropic /api/oauth/usage.
type ClaudeUsageWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// ClaudeUsageResponse contains the Anthropic OAuth usage windows.
type ClaudeUsageResponse struct {
	FiveHour struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"five_hour"`
	SevenDay struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"seven_day"`
	SevenDaySonnet struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"seven_day_sonnet"`
	// Fable's seven-day window corresponds to 7d_oi and the
	// seven_day_overage_included representative claim. GetUsage fills this from
	// passively sampled headers when the usage API omits it.
	SevenDayOverageIncluded ClaudeUsageWindow `json:"seven_day_overage_included"`
}
