package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ValidateJevCredentials requires API-key authentication and a nonempty key for Jev providers.
func ValidateJevCredentials(value *Record) error {
	if value == nil || value.Platform != PlatformJev {
		return nil
	}
	if value.Type != ProviderTypeAPIKey {
		return apperror.BadRequest("JEV_PROVIDER_TYPE_INVALID", "Jev providers require API Key credentials")
	}
	if strings.TrimSpace(value.GetCredential("api_key")) == "" {
		return apperror.BadRequest("JEV_API_KEY_REQUIRED", "Jev API key is required")
	}
	return nil
}

// GetJevBaseURL returns the configured endpoint or the default TypeSafe endpoint.
func (r *Record) GetJevBaseURL() string {
	if base := strings.TrimSpace(r.GetCredential("base_url")); base != "" {
		return base
	}
	return DefaultJevBaseURL
}
