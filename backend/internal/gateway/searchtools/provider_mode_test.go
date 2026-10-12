package searchtools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestGetWebSearchEmulationMode_Enabled(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "enabled"},
	}
	require.Equal(t, ModeEnabled, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_Disabled(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "disabled"},
	}
	require.Equal(t, ModeDisabled, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_Default(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "default"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_UnknownString(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "unknown"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_OldBoolTrue(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: true},
	}
	// Legacy true explicitly enables emulation.
	require.Equal(t, ModeEnabled, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_OldBoolFalse(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: false},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_NilProvider(t *testing.T) {
	var a *ProviderPolicy
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_NilExtra(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    nil,
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_MissingField(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_NonAnthropicPlatform(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "enabled"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

func TestGetWebSearchEmulationMode_NonAPIKeyType(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Extra:    map[string]any{FeatureKey: "enabled"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}
