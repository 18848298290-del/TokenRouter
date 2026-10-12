package capability

const (
	// ProviderTypeOAuth and the following constants identify provider authentication types.
	ProviderTypeOAuth          = "oauth"           // OAuth with profile and inference scopes.
	ProviderTypeSetupToken     = "setup-token"     // Setup token with inference scope.
	ProviderTypeAPIKey         = "apikey"          // API key authentication.
	ProviderTypeUpstream       = "upstream"        // Upstream passthrough using a base URL and API key.
	ProviderTypeBedrock        = "bedrock"         // Bedrock SigV4 or API key authentication, selected by credentials.auth_mode.
	ProviderTypeServiceAccount = "service_account" // Google service account for Vertex AI.
	ProviderTypeCosy           = "cosy"            // Qoder COSY authentication.

	OpenAIAuthModePersonalAccessToken = "personalAccessToken"
	OpenAIAuthModeAgentIdentity       = "agentIdentity"
)

// PolicyScopeMatches checks a rule scope against the provider authentication type.
func PolicyScopeMatches(scope string, isOAuth bool, isBedrock bool) bool {
	switch scope {
	case "all":
		return true
	case "oauth":
		return isOAuth
	case "apikey":
		return !isOAuth && !isBedrock
	case "bedrock":
		return isBedrock
	default:
		return true // Unknown scopes allow a match.
	}
}
