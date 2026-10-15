package dto

// settings_views.go defines JSON response views for request correction, beta
// rules, and service-tier settings.
// Runtime-setting responses expose correction toggles and policy rules using
// stable JSON field names. HTTP handlers build these views from the owning
// settings values; policy evaluation and input validation remain in the
// gateway, tier-policy, and upstream implementations that consume those values.

// RectifierSettings is the HTTP representation of request correction settings.
type RectifierSettings struct {
	// After successful settings reads, Enabled gates ThinkingSignatureEnabled
	// and ThinkingBudgetEnabled in RuntimeSettings accessors and
	// APIKeySignatureEnabled in Messages forwarding. Each path also consults its
	// specific feature toggle before enabling correction.
	Enabled                  bool `json:"enabled"`                    // global request correction toggle
	ThinkingSignatureEnabled bool `json:"thinking_signature_enabled"` // thinking signature correction enabled
	ThinkingBudgetEnabled    bool `json:"thinking_budget_enabled"`    // thinking budget correction enabled
	APIKeySignatureEnabled   bool `json:"apikey_signature_enabled"`   // API-key signature correction enabled
	// APIKeySignaturePatterns supplies API-key error patterns to signature matching.
	// RuntimeSettingsHandler returns an empty array when the setting's slice is nil.
	APIKeySignaturePatterns []string `json:"apikey_signature_patterns"`
}

// BetaPolicyRule is the HTTP representation of a beta-token policy rule.
// Action and ErrorMessage apply when ModelWhitelist is empty or matches the
// requested model. Otherwise FallbackAction and FallbackErrorMessage apply;
// an empty FallbackAction selects pass for that model.
//
// ErrorMessage and FallbackErrorMessage supply text for the selected block
// action. When that selected message is empty, the Anthropic evaluator produces
// a message naming the rejected BetaToken, for either model branch.
type BetaPolicyRule struct {
	BetaToken            string   `json:"beta_token"`                       // requested beta token
	Action               string   `json:"action"`                           // matching token action
	Scope                string   `json:"scope"`                            // provider authentication-kind filter
	ErrorMessage         string   `json:"error_message,omitempty"`          // matching branch block message
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // matching model patterns
	FallbackAction       string   `json:"fallback_action,omitempty"`        // unmatched model action
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // unmatched branch block message
}

// BetaPolicySettings contains beta-token rules for the HTTP API.
type BetaPolicySettings struct {
	Rules []BetaPolicyRule `json:"rules"` // configured beta policy rules
}

// OpenAIFastPolicyRule is the HTTP representation of an OpenAI Fast policy rule.
// After service-tier, user, and provider-scope matching, Action and ErrorMessage
// apply when ModelWhitelist is empty or matches the requested model. Otherwise
// FallbackAction and FallbackErrorMessage apply; an empty FallbackAction selects
// pass for that model.
//
// ErrorMessage and FallbackErrorMessage supply text for the selected block
// action. When that selected message is empty, tierpolicy.Resolve generates
// a message naming the service tier and model before returning the rejection.
type OpenAIFastPolicyRule struct {
	// ServiceTier selects a normalized tier. Empty values or the literal all
	// let tierpolicy.Evaluate consider the rule for every tier reaching evaluation.
	ServiceTier string `json:"service_tier"` // requested service tier
	Action      string `json:"action"`       // matching tier action
	Scope       string `json:"scope"`        // provider authentication-kind filter
	// UserIDs filters the user ID supplied to tierpolicy.Evaluate when nonempty.
	// Empty IDs define a global rule; the evaluator considers user-specific matches before that global set.
	UserIDs              []int64  `json:"user_ids,omitempty"`
	ErrorMessage         string   `json:"error_message,omitempty"`          // matching branch block message
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // matching model patterns
	FallbackAction       string   `json:"fallback_action,omitempty"`        // unmatched model action
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // unmatched branch block message
}

// OpenAIFastPolicySettings contains OpenAI Fast rules for the HTTP API.
type OpenAIFastPolicySettings struct {
	Rules []OpenAIFastPolicyRule `json:"rules"` // configured service-tier policy rules
}
