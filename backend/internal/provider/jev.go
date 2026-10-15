package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// jev.go validates Jev API Key credentials and resolves the configured or
// default endpoint.
// Jev decision requests use API Key credentials and a TypeSafe endpoint when no
// custom base URL is stored. Shared protocol normalization invokes Jev
// validation, while forwarding and model queries validate the selected URL
// before contacting it.

// ValidateJevCredentials rejects Jev records unless their type is API Key and
// their trimmed api_key is nonempty. Nil and other-platform records pass
// through so shared protocol normalization can call this validator.
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

// GetJevBaseURL returns the trimmed credentials.base_url when present,
// otherwise DefaultJevBaseURL. The caller supplies a nonnil record and performs
// URL trust validation before using the result for a request.
func (r *Record) GetJevBaseURL() string {
	if base := strings.TrimSpace(r.GetCredential("base_url")); base != "" {
		return base
	}
	return DefaultJevBaseURL
}
