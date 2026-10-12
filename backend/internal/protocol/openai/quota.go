package openai

// OpenAI quota observations carry primary and optional secondary windows with
// their own utilization, duration, and reset metadata. The reduced usage value
// also retains account identity and feature-specific limits, while provider-side
// querying fills its fetch timestamp and merges reset-credit details for display.

// OpenAIRateLimitWindow holds utilization, duration, and reset metadata for
// one quota window.
type OpenAIRateLimitWindow struct {
	UsedPercent        float64 `json:"used_percent"`         // percent
	LimitWindowSeconds int64   `json:"limit_window_seconds"` // seconds
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`  // seconds
	ResetAt            int64   `json:"reset_at"`             // Unix seconds
}

// OpenAIRateLimit holds the primary and optional secondary window status.
type OpenAIRateLimit struct {
	Allowed      bool `json:"allowed"`       // supplied admission flag
	LimitReached bool `json:"limit_reached"` // supplied exhaustion flag
	// PrimaryWindow and SecondaryWindow retain the upstream window positions.
	// Nil pointers represent missing objects, while supplied objects retain their
	// own durations and resets for provider-side quota classification and display.
	PrimaryWindow   *OpenAIRateLimitWindow `json:"primary_window,omitempty"`   // upstream primary window
	SecondaryWindow *OpenAIRateLimitWindow `json:"secondary_window,omitempty"` // optional secondary window
}

// OpenAIAdditionalRateLimit names a metered feature and its reported quota windows.
type OpenAIAdditionalRateLimit struct {
	LimitName      string           `json:"limit_name"`           // upstream limit name
	MeteredFeature string           `json:"metered_feature"`      // limited feature name
	RateLimit      *OpenAIRateLimit `json:"rate_limit,omitempty"` // optional feature windows
}

// OpenAIRateLimitResetCreditDetail exposes the expiry of one reset credit to the frontend.
type OpenAIRateLimitResetCreditDetail struct {
	ExpiresAt string `json:"expires_at,omitempty"` // retained expiry text
}

// OpenAIRateLimitResetCredits holds a count and reduced expiry details for quota display.
type OpenAIRateLimitResetCredits struct {
	// AvailableCount and Credits can originate from different count/detail
	// observations. OpenAIQuotaService prefers an explicit detail-response count,
	// otherwise its eligible-entry count when a detail list is present; expiry
	// validation and display filtering occur outside this value type.
	AvailableCount int                                `json:"available_count"`   // reported available count
	Credits        []OpenAIRateLimitResetCreditDetail `json:"credits,omitempty"` // retained expiry details
}

// OpenAIQuotaUsage is the subset of /wham/usage exposed to the frontend.
type OpenAIQuotaUsage struct {
	UserID                string                       `json:"user_id,omitempty"`                  // upstream user identifier
	ProviderID            string                       `json:"account_id,omitempty"`               // upstream account identifier
	Email                 string                       `json:"email,omitempty"`                    // upstream account email
	PlanType              string                       `json:"plan_type,omitempty"`                // upstream account plan
	RateLimit             *OpenAIRateLimit             `json:"rate_limit,omitempty"`               // optional account windows
	AdditionalRateLimits  []OpenAIAdditionalRateLimit  `json:"additional_rate_limits,omitempty"`   // feature-specific quota observations
	RateLimitResetCredits *OpenAIRateLimitResetCredits `json:"rate_limit_reset_credits,omitempty"` // optional reset-credit observations
	FetchedAt             int64                        `json:"fetched_at"`                         // Unix seconds
}

// OpenAIQuotaResetCredit holds the credit observation returned by the consume endpoint.
type OpenAIQuotaResetCredit struct {
	ID              string `json:"id,omitempty"`                // upstream credit identifier
	ResetType       string `json:"reset_type,omitempty"`        // upstream credit scope
	Status          string `json:"status,omitempty"`            // upstream credit status
	GrantedAt       string `json:"granted_at,omitempty"`        // supplied grant timestamp
	ExpiresAt       string `json:"expires_at,omitempty"`        // supplied expiry timestamp
	RedeemStartedAt string `json:"redeem_started_at,omitempty"` // supplied redemption-start timestamp
	RedeemedAt      string `json:"redeemed_at,omitempty"`       // supplied redemption-completion timestamp
}

// OpenAIQuotaResetResult contains the outcome and credit returned by
// /wham/rate-limit-reset-credits/consume.
type OpenAIQuotaResetResult struct {
	Code         string                  `json:"code"`             // upstream result code
	Credit       *OpenAIQuotaResetCredit `json:"credit,omitempty"` // optional returned credit
	WindowsReset int                     `json:"windows_reset"`    // reset window count
}
