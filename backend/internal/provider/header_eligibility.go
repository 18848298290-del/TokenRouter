package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

const credKeyHeaderOverrideEnabled = "header_override_enabled"

// IsHeaderOverrideEligible reports whether this provider type supports request header overrides.
// Anthropic, OpenAI, Kimi, Zhipu, DeepSeek, and Jev support API keys; Grok also supports OAuth.
// Overrides can supply relay access headers when subscription traffic uses a custom endpoint.
func (r *Record) IsHeaderOverrideEligible() bool {
	if r == nil {
		return false
	}
	switch r.Platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformJev:
		return r.Type == ProviderTypeAPIKey
	case PlatformGrok:
		return r.Type == ProviderTypeAPIKey || r.Type == ProviderTypeOAuth
	default:
		return false
	}
}

// IsHeaderOverrideEnabled reports whether an eligible provider has enabled header overrides.
func (r *Record) IsHeaderOverrideEnabled() bool {
	if !r.IsHeaderOverrideEligible() || r.Credentials == nil {
		return false
	}
	enabled, ok := r.Credentials[credKeyHeaderOverrideEnabled].(bool)
	return ok && enabled
}

// HeaderOverrides returns an independent map of eligible overrides validated by egress.
// It returns nil when overrides are disabled or unsupported.
func (r *Record) HeaderOverrides() map[string]string {
	if !r.IsHeaderOverrideEnabled() {
		return nil
	}
	return egress.ResolveHeaderOverrides(StringMappingFromRaw(r.Credentials["header_overrides"]))
}
