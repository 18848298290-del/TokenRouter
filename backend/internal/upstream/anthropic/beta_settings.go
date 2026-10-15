package anthropic

// beta_settings.go defines beta policy settings and the default model
// whitelist for context-1m requests.
// Beta policy defaults permit the context-1m token for the listed Sonnet 5
// spellings and filter it for other models. The table includes direct, Vertex,
// and Bedrock forms; delimiter-specific patterns keep similarly named model
// families outside that permission.

// Beta policy actions decide pass, removal, or rejection, while scopes select
// the provider authentication flags considered during rule evaluation.
const (
	BetaPolicyActionPass   = "pass"   // Leave handling to later request policy.
	BetaPolicyActionFilter = "filter" // Contribute the token to the Header drop set.
	BetaPolicyActionBlock  = "block"  // Report a matching rejection.

	BetaPolicyScopeAll     = "all"     // All provider types.
	BetaPolicyScopeOAuth   = "oauth"   // OAuth provider scope.
	BetaPolicyScopeAPIKey  = "apikey"  // Neither OAuth nor Bedrock.
	BetaPolicyScopeBedrock = "bedrock" // AWS Bedrock providers.
)

// BetaPolicyRule configures token handling by provider scope and model match.
type BetaPolicyRule struct {
	BetaToken string `json:"beta_token"` // exact beta token
	// Action and ErrorMessage apply when ModelWhitelist is empty or matches.
	// FallbackAction and FallbackErrorMessage apply outside that whitelist, with
	// an empty fallback resolving to pass.
	Action string `json:"action"` // primary rule action
	// Scope selects provider combinations through PolicyScopeMatches; an unknown
	// scope string matches all combinations there, just like the all scope.
	Scope                string   `json:"scope"`                            // provider authentication scope
	ErrorMessage         string   `json:"error_message,omitempty"`          // primary block message
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // primary action models
	FallbackAction       string   `json:"fallback_action,omitempty"`        // unmatched model action
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // unmatched block message
}

// BetaPolicySettings holds ordered rules for Anthropic request capability
// decisions.
type BetaPolicySettings struct {
	// Rules are evaluated in order, which determines the first block message while
	// all matching filter actions can contribute tokens.
	Rules []BetaPolicyRule `json:"rules"` // ordered policy rules
}

// DefaultBetaPolicySettings returns fresh rules that filter fast mode and
// restrict context-1m to listed Sonnet 5 model spellings. Nonmatching models
// use the filter fallback; patterns include delimited direct, Vertex, and
// Bedrock variants.
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
