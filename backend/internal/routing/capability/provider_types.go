package capability

// Provider authentication types participate in native protocol eligibility
// separately from platform names. Scoped rules use the caller's OAuth and
// Bedrock classification, and unrecognized saved scope values continue to match
// so callers needing rejection validate that scope before matching.

// Provider authentication identifiers select capability options and scoped
// policy; OpenAI mode identifiers further distinguish OAuth protocol
// availability.
const (
	ProviderTypeOAuth      = "oauth"       // OAuth provider authentication
	ProviderTypeSetupToken = "setup-token" // inference setup-token authentication
	ProviderTypeAPIKey     = "apikey"      // static API-key authentication
	// legacy upstream passthrough authentication.
	ProviderTypeUpstream = "upstream"
	ProviderTypeBedrock  = "bedrock" // Bedrock provider authentication
	// Google service-account authentication.
	ProviderTypeServiceAccount = "service_account"
	ProviderTypeCosy           = "cosy" // Qoder COSY provider authentication

	// OpenAIAuthModePersonalAccessToken and OpenAIAuthModeAgentIdentity
	// distinguish OAuth credential modes. NativeProtocolOptions uses them to limit
	// Alpha Search and Live support within an OAuth provider type.
	//
	// OpenAIAuthModePersonalAccessToken: personal-access-token authentication
	// mode.
	OpenAIAuthModePersonalAccessToken = "personalAccessToken"
	// OpenAI agent-identity OAuth mode.
	OpenAIAuthModeAgentIdentity = "agentIdentity"
)

// PolicyScopeMatches evaluates all, OAuth, API-key-like, or Bedrock scopes
// using the supplied authentication flags. API-key scope means neither OAuth
// nor Bedrock; an unknown scope permits a match.
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
		// Saved scopes outside the recognized labels remain permissive, allowing the
		// same match as all; callers needing unknown-scope rejection validate it
		// before this step.
		return true // unrecognized scope remains permissive
	}
}
