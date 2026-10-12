package dto

// Provider health settings expose cooldown durations in the units used by their
// administration endpoints. OpenAI 403 and stream-timeout policies additionally
// include threshold counts and observation windows, while runtime settings
// supplies defaults and validation before these display values are encoded.

// OverloadCooldownSettings displays enablement and duration for provider
// overload avoidance.
type OverloadCooldownSettings struct {
	Enabled         bool `json:"enabled"`          // configured policy enablement flag
	CooldownMinutes int  `json:"cooldown_minutes"` // minutes
}

// OpenAI403CooldownSettings displays provider cooldown and
// repeated-forbidden-response escalation settings.
type OpenAI403CooldownSettings struct {
	Enabled                 bool `json:"enabled"`                    // configured policy enablement flag
	CooldownMinutes         int  `json:"cooldown_minutes"`           // minutes
	ErrorOnThresholdEnabled bool `json:"error_on_threshold_enabled"` // repeated-error escalation enablement flag
	ThresholdCount          int  `json:"threshold_count"`            // configured observed-event count threshold
	ThresholdWindowMinutes  int  `json:"threshold_window_minutes"`   // minutes
}

// RateLimit429CooldownSettings displays enablement and duration for default 429
// avoidance.
type RateLimit429CooldownSettings struct {
	Enabled         bool `json:"enabled"`          // configured policy enablement flag
	CooldownSeconds int  `json:"cooldown_seconds"` // seconds
}

// OpenAIImagesOAuthUnavailableCooldownSettings displays the duration of OAuth
// image-unavailability avoidance.
type OpenAIImagesOAuthUnavailableCooldownSettings struct {
	CooldownMinutes int `json:"cooldown_minutes"` // minutes
}

// StreamTimeoutSettings displays the action and repeated-event window for
// streaming timeouts.
type StreamTimeoutSettings struct {
	Enabled                bool   `json:"enabled"`                  // configured policy enablement flag
	Action                 string `json:"action"`                   // stream-timeout handling action selector
	TempUnschedMinutes     int    `json:"temp_unsched_minutes"`     // minutes
	ThresholdCount         int    `json:"threshold_count"`          // configured observed-event count threshold
	ThresholdWindowMinutes int    `json:"threshold_window_minutes"` // minutes
}
