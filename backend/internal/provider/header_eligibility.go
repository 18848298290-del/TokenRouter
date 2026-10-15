package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// header_eligibility.go checks header-override eligibility and resolves
// enabled provider headers through egress filtering.
// Provider Header overrides are available for selected platform and credential
// types. The enabled flag is read as a boolean, then egress filters stored names
// and values before a new request map is returned, keeping administrator
// configuration separate from outgoing headers.

// credKeyHeaderOverrideEnabled selects the saved boolean flag checked after
// platform and credential-type eligibility before outgoing overrides are resolved.
const credKeyHeaderOverrideEnabled = "header_override_enabled"

// IsHeaderOverrideEligible reports whether administrator Header overrides apply
// to this provider. API Keys qualify on Anthropic, OpenAI, Kimi, Zhipu,
// DeepSeek, Jev and Grok; Grok OAuth also qualifies, while nil records return
// false.
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

// IsHeaderOverrideEnabled reports whether an eligible provider has a true
// boolean override flag. Missing credentials, unsupported providers and flags
// stored with another type return false before Header values are read.
func (r *Record) IsHeaderOverrideEnabled() bool {
	if !r.IsHeaderOverrideEligible() || r.Credentials == nil {
		return false
	}
	enabled, ok := r.Credentials[credKeyHeaderOverrideEnabled].(bool)
	return ok && enabled
}

// HeaderOverrides returns a new map of enabled overrides accepted by egress
// validation. Unsupported, disabled or entirely filtered configuration returns
// nil; callers keep credentials stable during synchronous filtering.
func (r *Record) HeaderOverrides() map[string]string {
	if !r.IsHeaderOverrideEnabled() {
		return nil
	}
	return egress.ResolveHeaderOverrides(StringMappingFromRaw(r.Credentials["header_overrides"]))
}
