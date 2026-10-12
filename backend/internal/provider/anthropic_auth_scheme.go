package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// API-key Messages authentication can use x-api-key or a bearer header for
// Anthropic and CN providers. Provider Extra selects the scheme for those
// API-key records; other configurations keep the x-api-key result returned by
// this helper before forwarding constructs its headers.

// Authentication scheme values identify the Extra setting and the header
// variant consumed by Messages forwarding.
const (
	anthropicAPIKeyAuthSchemeExtraKey            = "anthropic_apikey_auth_scheme" // configured authentication scheme key
	AnthropicAPIKeyAuthSchemeXAPIKey             = "x_api_key"                    // x-api-key header authentication
	AnthropicAPIKeyAuthSchemeAuthorizationBearer = "authorization_bearer"         // Authorization bearer header authentication
)

// GetAnthropicAPIKeyAuthScheme selects the Messages authentication scheme for
// Anthropic or CN API-key records. A trimmed authorization_bearer value selects
// bearer authentication; nil records, other providers, and unrecognized values
// select the x-api-key scheme.
func (r *Record) GetAnthropicAPIKeyAuthScheme() string {
	if r == nil || r.Type != capability.ProviderTypeAPIKey {
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}
	if r.Platform != capability.PlatformAnthropic && !r.IsCNProvider() {
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}

	switch strings.TrimSpace(r.GetExtraString(anthropicAPIKeyAuthSchemeExtraKey)) {
	case AnthropicAPIKeyAuthSchemeAuthorizationBearer:
		return AnthropicAPIKeyAuthSchemeAuthorizationBearer
	default:
		// Unknown settings do not identify a supported bearer scheme. Return the
		// x-api-key variant so Messages forwarding selects its registered header encoding.
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}
}
