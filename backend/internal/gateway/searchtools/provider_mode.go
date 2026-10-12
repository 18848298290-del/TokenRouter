package searchtools

import "github.com/TokenFlux/TokenRouter/internal/routing/capability"

const (
	ModeDefault  = "default"
	ModeEnabled  = "enabled"
	ModeDisabled = "disabled"
)

// ProviderPolicy holds the provider settings used to enable search emulation.
type ProviderPolicy struct {
	ID       int64
	Platform string
	Type     string
	Extra    map[string]any
}

// ModeSelection exposes legacy boolean values for the caller to diagnose.
type ModeSelection struct {
	Mode       string
	LegacyBool *bool
}

func ProviderMode(value *ProviderPolicy) ModeSelection {
	result := ModeSelection{Mode: ModeDefault}
	if value == nil || value.Platform != capability.PlatformAnthropic || value.Type != capability.ProviderTypeAPIKey || value.Extra == nil {
		return result
	}
	raw := value.Extra[FeatureKey]
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
