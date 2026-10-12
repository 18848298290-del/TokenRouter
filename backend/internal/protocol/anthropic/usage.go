package anthropic

// Claude's OAuth usage endpoint reports utilization and reset timestamps for
// several rolling quota windows. The wire response retains the timestamp text
// for provider-side parsing, where the five-hour window is displayed even when
// its reset time is absent and optional weekly windows require reset information.

// ClaudeUsageWindow is one window returned by Anthropic /api/oauth/usage.
type ClaudeUsageWindow struct {
	Utilization float64 `json:"utilization"` // percent
	ResetsAt    string  `json:"resets_at"`   // upstream reset timestamp
}

// ClaudeUsageResponse holds rolling quota observations returned by Claude's usage endpoint.
type ClaudeUsageResponse struct {
	// FiveHour supplies the short quota window; BuildUsageInfo includes its
	// utilization even when the upstream omits the corresponding reset timestamp.
	FiveHour struct {
		Utilization float64 `json:"utilization"` // percent
		ResetsAt    string  `json:"resets_at"`   // upstream reset timestamp
	} `json:"five_hour"`
	// SevenDay supplies the general weekly quota window; BuildUsageInfo includes
	// this window when its reset timestamp is present in the response.
	SevenDay struct {
		Utilization float64 `json:"utilization"` // percent
		ResetsAt    string  `json:"resets_at"`   // upstream reset timestamp
	} `json:"seven_day"`
	// SevenDaySonnet supplies the Sonnet weekly quota; BuildUsageInfo includes
	// this window when its reset timestamp is present in the response.
	SevenDaySonnet struct {
		Utilization float64 `json:"utilization"` // percent
		ResetsAt    string  `json:"resets_at"`   // upstream reset timestamp
	} `json:"seven_day_sonnet"`
	// SevenDayOverageIncluded holds the weekly Fable observation, corresponding
	// to the 7d_oi header window. BuildUsageInfo exposes it when ResetsAt is present.
	SevenDayOverageIncluded ClaudeUsageWindow `json:"seven_day_overage_included"`
}
