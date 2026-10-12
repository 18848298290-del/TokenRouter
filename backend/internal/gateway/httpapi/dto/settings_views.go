package dto

// RectifierSettings is the HTTP representation of request correction settings.
type RectifierSettings struct {
	Enabled                  bool     `json:"enabled"`
	ThinkingSignatureEnabled bool     `json:"thinking_signature_enabled"`
	ThinkingBudgetEnabled    bool     `json:"thinking_budget_enabled"`
	APIKeySignatureEnabled   bool     `json:"apikey_signature_enabled"`
	APIKeySignaturePatterns  []string `json:"apikey_signature_patterns"`
}

// BetaPolicyRule is the HTTP representation of a beta-token policy rule.
type BetaPolicyRule struct {
	BetaToken            string   `json:"beta_token"`
	Action               string   `json:"action"`
	Scope                string   `json:"scope"`
	ErrorMessage         string   `json:"error_message,omitempty"`
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`
	FallbackAction       string   `json:"fallback_action,omitempty"`
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"`
}

// BetaPolicySettings contains beta-token rules for the HTTP API.
type BetaPolicySettings struct {
	Rules []BetaPolicyRule `json:"rules"`
}

// OpenAIFastPolicyRule is the HTTP representation of an OpenAI Fast policy rule.
type OpenAIFastPolicyRule struct {
	ServiceTier          string   `json:"service_tier"`
	Action               string   `json:"action"`
	Scope                string   `json:"scope"`
	UserIDs              []int64  `json:"user_ids,omitempty"`
	ErrorMessage         string   `json:"error_message,omitempty"`
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`
	FallbackAction       string   `json:"fallback_action,omitempty"`
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"`
}

// OpenAIFastPolicySettings contains OpenAI Fast rules for the HTTP API.
type OpenAIFastPolicySettings struct {
	Rules []OpenAIFastPolicyRule `json:"rules"`
}
