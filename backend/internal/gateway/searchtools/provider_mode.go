package searchtools

import "github.com/TokenFlux/TokenRouter/internal/routing/capability"

// Anthropic API-key providers interpret the emulation feature through exact
// enabled/disabled labels or a legacy bool. The legacy false value retains
// default mode, allowing the later group feature to decide execution after
// global prerequisites.
//
// ProviderMode also returns copied legacy bool evidence so its caller can
// diagnose that spelling independently of the chosen enum. Missing, unknown or
// inapplicable provider values yield default without that bool evidence. The
// provider settings map is read without mutation, and ShouldEmulate separately
// checks manager availability and the global gate before applying mode. This
// preserves the distinction between a default choice and an explicit disabled
// string when group policy is enabled.

// These mode labels follow, override or decline group feature lookup after
// emulation has passed manager availability and the global feature gate.
const (
	ModeDefault  = "default"  // follow selected group feature policy
	ModeEnabled  = "enabled"  // allow after global feature gating
	ModeDisabled = "disabled" // decline emulation for this provider
)

// ProviderPolicy supplies candidate metadata and shared settings for
// provider-mode interpretation.
type ProviderPolicy struct {
	ID       int64  // selected gateway-provider identifier
	Platform string // selected provider platform spelling
	Type     string // selected provider credential type
	// Extra is read for FeatureKey only on an Anthropic API-key provider. Callers
	// keep its map stable while mode is interpreted.
	Extra map[string]any // shared provider feature configuration
}

// ModeSelection reports the chosen mode and optional copied legacy bool
// evidence.
type ModeSelection struct {
	Mode string // interpreted provider policy label
	// LegacyBool is present when FeatureKey stored a bool. It points to a copied
	// scalar, so later mutation leaves ProviderPolicy.Extra unchanged.
	LegacyBool *bool // copied legacy feature boolean
}

// ProviderMode interprets FeatureKey for an exact Anthropic API-key provider
// and otherwise returns default. Exact enabled/disabled strings override
// default; missing, unknown and wrong-type values retain it.
//
// Legacy bool evidence is copied for diagnosis. True selects enabled, while
// false retains default and can still be allowed by later group policy.
// Reading leaves the supplied map intact, and this interpretation alone
// bypasses neither manager availability nor the global gate applied by
// ShouldEmulate.
func ProviderMode(value *ProviderPolicy) ModeSelection {
	result := ModeSelection{Mode: ModeDefault}
	if value == nil || value.Platform != capability.PlatformAnthropic || value.Type != capability.ProviderTypeAPIKey || value.Extra == nil {
		return result
	}
	raw := value.Extra[FeatureKey]
	// Copy bool evidence for diagnostics while retaining false as default, so
	// this legacy spelling still permits the group-policy decision that follows.
	if legacy, ok := raw.(bool); ok {
		result.LegacyBool = &legacy
		if legacy {
			result.Mode = ModeEnabled
		}
		return result
	}
	mode, ok := raw.(string)
	if !ok {
		return result
	}
	switch mode {
	case ModeEnabled, ModeDisabled:
		result.Mode = mode
	}
	return result
}
