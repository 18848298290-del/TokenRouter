package dto

// settings_views.go defines the JSON response for administrator panel
// rate-limit settings.
// Administrators edit panel request limits as one JSON object. Read and update
// handlers return the same five fields so the settings page can display the
// stored limits and administrator exemption after a save.

// PanelRateLimitSettings is the JSON representation of panel rate limits.
type PanelRateLimitSettings struct {
	Enabled bool `json:"enabled"` // panel limit enable flag
	// UserRPM, HeavyRPM, and PublicIPRPM are requests per minute for their
	// respective buckets. Zero disables that bucket when the panel limiter reads it.
	UserRPM     int  `json:"user_rpm"`      // authenticated user's aggregate limit
	HeavyRPM    int  `json:"heavy_rpm"`     // authenticated user's heavy-query limit
	ExemptAdmin bool `json:"exempt_admin"`  // administrator user-bucket exemption flag
	PublicIPRPM int  `json:"public_ip_rpm"` // unauthenticated public-address limit
}
