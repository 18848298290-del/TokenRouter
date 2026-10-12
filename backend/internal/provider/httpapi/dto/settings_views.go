package dto

// OverloadCooldownSettings is the HTTP representation of overload cooldown settings.
type OverloadCooldownSettings struct {
	Enabled         bool `json:"enabled"`
	CooldownMinutes int  `json:"cooldown_minutes"`
}

// OpenAI403CooldownSettings is the HTTP representation of OpenAI 403 cooldown settings.
type OpenAI403CooldownSettings struct {
	Enabled                 bool `json:"enabled"`
	CooldownMinutes         int  `json:"cooldown_minutes"`
	ErrorOnThresholdEnabled bool `json:"error_on_threshold_enabled"`
	ThresholdCount          int  `json:"threshold_count"`
	ThresholdWindowMinutes  int  `json:"threshold_window_minutes"`
}

// RateLimit429CooldownSettings is the HTTP representation of 429 cooldown settings.
type RateLimit429CooldownSettings struct {
	Enabled         bool `json:"enabled"`
	CooldownSeconds int  `json:"cooldown_seconds"`
}

// OpenAIImagesOAuthUnavailableCooldownSettings configures cooldown after OAuth image unavailability.
type OpenAIImagesOAuthUnavailableCooldownSettings struct {
	CooldownMinutes int `json:"cooldown_minutes"`
}

// StreamTimeoutSettings is the HTTP representation of streaming timeout settings.
type StreamTimeoutSettings struct {
	Enabled                bool   `json:"enabled"`
	Action                 string `json:"action"`
	TempUnschedMinutes     int    `json:"temp_unsched_minutes"`
	ThresholdCount         int    `json:"threshold_count"`
	ThresholdWindowMinutes int    `json:"threshold_window_minutes"`
}
