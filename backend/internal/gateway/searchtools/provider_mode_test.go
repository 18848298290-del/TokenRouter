package searchtools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestGetWebSearchEmulationMode_Enabled stores an enabled string on an
// Anthropic API-key provider. The returned mode must be enabled for that exact
// feature spelling and metadata combination.
func TestGetWebSearchEmulationMode_Enabled(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "enabled"},
	}
	require.Equal(t, ModeEnabled, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_Disabled stores a disabled string on an
// Anthropic API-key provider. The returned mode must be disabled so the later
// emulator gate can distinguish this override from default.
func TestGetWebSearchEmulationMode_Disabled(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "disabled"},
	}
	require.Equal(t, ModeDisabled, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_Default stores the explicit default string
// under FeatureKey. Interpretation must retain default mode for this supported
// provider metadata rather than treating field presence as enabled.
func TestGetWebSearchEmulationMode_Default(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "default"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_UnknownString stores an unrecognized string
// under FeatureKey. Interpretation must return default mode for that input,
// leaving unsupported spelling distinct from either recognized override label.
func TestGetWebSearchEmulationMode_UnknownString(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "unknown"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_OldBoolTrue stores true using the legacy
// boolean feature representation. Its mode assertion must return enabled for
// the supplied Anthropic API-key provider rather than falling through to
// default.
func TestGetWebSearchEmulationMode_OldBoolTrue(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: true},
	}
	// Legacy true explicitly enables emulation.
	require.Equal(t, ModeEnabled, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_OldBoolFalse stores false using the legacy
// boolean feature representation. The returned mode must remain default,
// protecting this value's interpretation separately from the explicit disabled
// string.
func TestGetWebSearchEmulationMode_OldBoolFalse(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: false},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_NilProvider supplies a nil provider pointer to
// mode interpretation. The returned mode must be default when no platform,
// credential type or feature settings are available to read.
func TestGetWebSearchEmulationMode_NilProvider(t *testing.T) {
	var a *ProviderPolicy
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_NilExtra provides valid Anthropic API-key
// metadata with a nil settings map. Interpretation must return default instead
// of requiring a feature lookup from unavailable provider settings.
func TestGetWebSearchEmulationMode_NilExtra(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    nil,
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_MissingField provides an allocated settings
// map whose feature entry is absent. Interpretation must return default for
// that provider, matching the missing-configuration case without inventing an
// override.
func TestGetWebSearchEmulationMode_MissingField(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_NonAnthropicPlatform places an enabled feature
// on OpenAI provider metadata. Interpretation must retain default because this
// helper applies the feature representation to the Anthropic platform only.
func TestGetWebSearchEmulationMode_NonAnthropicPlatform(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Extra:    map[string]any{FeatureKey: "enabled"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}

// TestGetWebSearchEmulationMode_NonAPIKeyType places an enabled feature on an
// Anthropic OAuth provider. Interpretation must retain default because this
// helper reads override settings only for the API-key credential type.
func TestGetWebSearchEmulationMode_NonAPIKeyType(t *testing.T) {
	a := &ProviderPolicy{
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Extra:    map[string]any{FeatureKey: "enabled"},
	}
	require.Equal(t, ModeDefault, ProviderMode(a).Mode)
}
