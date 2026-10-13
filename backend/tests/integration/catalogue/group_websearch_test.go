package catalogue_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/searchtools"
	"github.com/TokenFlux/TokenRouter/internal/routing/testkit"
)

// Web-search emulation reads a boolean from the requested platform's feature
// object. Configuration delegates to GroupPolicyView, whose lookup rejects a bare
// boolean and incorrectly typed entries; each fixture isolates one accepted or
// disabled input shape.

// TestGroupPolicy_IsWebSearchEmulationEnabled_Enabled supplies an Anthropic true
// entry in the web-search feature object. The requested Anthropic platform must
// observe emulation as enabled.
func TestGroupPolicy_IsWebSearchEmulationEnabled_Enabled(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: map[string]any{"anthropic": true},
		},
	}
	require.True(t, c.IsWebSearchEmulationEnabled("anthropic"))
}

// TestGroupPolicy_IsWebSearchEmulationEnabled_DifferentPlatform supplies an
// Anthropic true entry and queries the OpenAI platform. The absent OpenAI entry
// must produce a disabled result for that lookup.
func TestGroupPolicy_IsWebSearchEmulationEnabled_DifferentPlatform(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: map[string]any{"anthropic": true},
		},
	}
	require.False(t, c.IsWebSearchEmulationEnabled("openai"))
}

// TestGroupPolicy_IsWebSearchEmulationEnabled_Disabled supplies an Anthropic
// false entry in the web-search feature object. The requested platform must
// observe that saved value as disabled.
func TestGroupPolicy_IsWebSearchEmulationEnabled_Disabled(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: map[string]any{"anthropic": false},
		},
	}
	require.False(t, c.IsWebSearchEmulationEnabled("anthropic"))
}

// TestGroupPolicy_IsWebSearchEmulationEnabled_NilFeaturesConfig supplies a
// Configuration with a nil feature map. The Anthropic lookup must return false
// when its feature object is absent.
func TestGroupPolicy_IsWebSearchEmulationEnabled_NilFeaturesConfig(t *testing.T) {
	c := &testkit.Configuration{FeaturesConfig: nil}
	require.False(t, c.IsWebSearchEmulationEnabled("anthropic"))
}

// TestGroupPolicy_IsWebSearchEmulationEnabled_NilGroupPolicy invokes the
// web-search helper on a nil Configuration pointer. The nil receiver must return
// false for the requested Anthropic platform.
func TestGroupPolicy_IsWebSearchEmulationEnabled_NilGroupPolicy(t *testing.T) {
	var c *testkit.Configuration
	require.False(t, c.IsWebSearchEmulationEnabled("anthropic"))
}

// TestGroupPolicy_IsWebSearchEmulationEnabled_WrongStructure stores a bare
// boolean where the reader expects a per-platform object. The Anthropic lookup
// must return false for that input shape.
func TestGroupPolicy_IsWebSearchEmulationEnabled_WrongStructure(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: true, // bare boolean feature input
		},
	}
	require.False(t, c.IsWebSearchEmulationEnabled("anthropic"))
}

// TestGroupPolicy_IsWebSearchEmulationEnabled_PlatformValueNotBool stores a
// string in the requested platform's feature entry. The reader must return false
// because the entry cannot provide a boolean value.
func TestGroupPolicy_IsWebSearchEmulationEnabled_PlatformValueNotBool(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			searchtools.FeatureKey: map[string]any{"anthropic": "yes"},
		},
	}
	require.False(t, c.IsWebSearchEmulationEnabled("anthropic"))
}
