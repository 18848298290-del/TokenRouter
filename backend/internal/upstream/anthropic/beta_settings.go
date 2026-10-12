package anthropic

// Beta policy actions and provider scopes.
const (
	BetaPolicyActionPass   = "pass"   // Pass the token through.
	BetaPolicyActionFilter = "filter" // Remove the token from the beta header.
	BetaPolicyActionBlock  = "block"  // Reject the request.

	BetaPolicyScopeAll     = "all"     // All provider types.
	BetaPolicyScopeOAuth   = "oauth"   // OAuth providers.
	BetaPolicyScopeAPIKey  = "apikey"  // API Key providers.
	BetaPolicyScopeBedrock = "bedrock" // AWS Bedrock providers.
)

// BetaPolicyRule applies an action to one beta token.
type BetaPolicyRule struct {
	BetaToken            string   `json:"beta_token"`
	Action               string   `json:"action"`                           // "pass" | "filter" | "block"
	Scope                string   `json:"scope"`                            // "all" | "oauth" | "apikey" | "bedrock"
	ErrorMessage         string   `json:"error_message,omitempty"`          // Custom message for action=block.
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // Model patterns; empty applies to every model.
	FallbackAction       string   `json:"fallback_action,omitempty"`        // Action for models outside ModelWhitelist.
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // Custom message for fallback_action=block.
}

// BetaPolicySettings contains the configured beta rules.
type BetaPolicySettings struct {
	Rules []BetaPolicyRule `json:"rules"`
}

// DefaultBetaPolicySettings filters fast mode and permits the 1M beta for the
// listed Sonnet 5 forms. Other models receive the filter action.
//
// The whitelist covers direct API names, Vertex date suffixes, and Bedrock
// inference IDs. Exact names and delimited suffix patterns avoid matching
// other names such as claude-sonnet-50 or claude-sonnet-5.1.
func DefaultBetaPolicySettings() *BetaPolicySettings {
	return &BetaPolicySettings{
		Rules: []BetaPolicyRule{
			{
				BetaToken: "fast-mode-2026-02-01",
				Action:    BetaPolicyActionFilter,
				Scope:     BetaPolicyScopeAll,
			},
			{
				BetaToken: "context-1m-2025-08-07",
				Action:    BetaPolicyActionPass,
				Scope:     BetaPolicyScopeAll,
				ModelWhitelist: []string{
					// Direct Anthropic model names.
					"claude-sonnet-5",
					"claude-sonnet-5-*",
					// Vertex date suffixes use @YYYYMMDD.
					"claude-sonnet-5@*",
					// AWS Bedrock cross-region inference profile
					// Unsuffixed IDs need exact entries alongside the suffix patterns.
					"us.anthropic.claude-sonnet-5",
					"us.anthropic.claude-sonnet-5-v*",
					"us.anthropic.claude-sonnet-5-*",
					"eu.anthropic.claude-sonnet-5",
					"eu.anthropic.claude-sonnet-5-v*",
					"eu.anthropic.claude-sonnet-5-*",
					"apac.anthropic.claude-sonnet-5-v*",
					"apac.anthropic.claude-sonnet-5-*",
					"jp.anthropic.claude-sonnet-5-v*",
					"jp.anthropic.claude-sonnet-5-*",
					"au.anthropic.claude-sonnet-5",
					"au.anthropic.claude-sonnet-5-v*",
					"au.anthropic.claude-sonnet-5-*",
					"us-gov.anthropic.claude-sonnet-5-v*",
					"us-gov.anthropic.claude-sonnet-5-*",
					"global.anthropic.claude-sonnet-5",
					"global.anthropic.claude-sonnet-5-v*",
					"global.anthropic.claude-sonnet-5-*",
					// Bedrock IDs without a cross-region prefix.
					"anthropic.claude-sonnet-5",
					"anthropic.claude-sonnet-5-v*",
					"anthropic.claude-sonnet-5-*",
				},
				FallbackAction: BetaPolicyActionFilter,
			},
		},
	}
}
