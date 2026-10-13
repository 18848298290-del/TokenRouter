package catalogue_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/testkit"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
)

// Bedrock Claude Code compatibility accepts a global boolean or legacy
// per-platform maps. Configuration delegates the flag lookup to GroupPolicyView;
// these fixtures distinguish accepted true values from missing, nil, false, and
// incorrectly typed inputs.

// TestGroupPolicy_IsBedrockCCCompatEnabled_Enabled supplies a true compatibility
// boolean through the combined policy fixture. The Bedrock lookup must report
// enabled for that saved flag.
func TestGroupPolicy_IsBedrockCCCompatEnabled_Enabled(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			bedrock.FeatureKeyBedrockCCCompat: true,
		},
	}
	require.True(t, c.IsBedrockCCCompatEnabled("bedrock"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_AppliesToAllPlatforms supplies the
// global compatibility boolean and queries several platform names. Anthropic,
// OpenAI, and an empty platform must all observe the enabled flag.
func TestGroupPolicy_IsBedrockCCCompatEnabled_AppliesToAllPlatforms(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			bedrock.FeatureKeyBedrockCCCompat: true,
		},
	}
	require.True(t, c.IsBedrockCCCompatEnabled("anthropic"))
	require.True(t, c.IsBedrockCCCompatEnabled("openai"))
	require.True(t, c.IsBedrockCCCompatEnabled(""))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_Disabled supplies a false global
// compatibility boolean in the policy fixture. The Bedrock lookup must report
// disabled for that saved value.
func TestGroupPolicy_IsBedrockCCCompatEnabled_Disabled(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			bedrock.FeatureKeyBedrockCCCompat: false,
		},
	}
	require.False(t, c.IsBedrockCCCompatEnabled("bedrock"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_NilFeaturesConfig supplies a
// configuration whose feature map is nil. The compatibility lookup must return
// false when that feature source contains no values.
func TestGroupPolicy_IsBedrockCCCompatEnabled_NilFeaturesConfig(t *testing.T) {
	c := &testkit.Configuration{FeaturesConfig: nil}
	require.False(t, c.IsBedrockCCCompatEnabled("bedrock"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_NilGroupPolicy invokes the
// compatibility helper on a nil Configuration pointer. The nil receiver must
// produce a disabled result for the Bedrock platform.
func TestGroupPolicy_IsBedrockCCCompatEnabled_NilGroupPolicy(t *testing.T) {
	var c *testkit.Configuration
	require.False(t, c.IsBedrockCCCompatEnabled("bedrock"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_WrongType stores a string under the
// compatibility feature key. The lookup must return false because that value
// cannot supply a boolean override.
func TestGroupPolicy_IsBedrockCCCompatEnabled_WrongType(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			bedrock.FeatureKeyBedrockCCCompat: "yes",
		},
	}
	require.False(t, c.IsBedrockCCCompatEnabled("bedrock"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_OldMapFormat supplies a legacy
// map[string]any containing an Anthropic boolean. Anthropic must observe true,
// while an absent OpenAI entry must remain disabled.
func TestGroupPolicy_IsBedrockCCCompatEnabled_OldMapFormat(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			bedrock.FeatureKeyBedrockCCCompat: map[string]any{"anthropic": true},
		},
	}
	require.True(t, c.IsBedrockCCCompatEnabled("anthropic"))
	require.False(t, c.IsBedrockCCCompatEnabled("openai"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_OldBoolMapFormat supplies a legacy
// map[string]bool containing an Anthropic flag. The reader must accept that map
// and report false for the absent OpenAI entry.
func TestGroupPolicy_IsBedrockCCCompatEnabled_OldBoolMapFormat(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			bedrock.FeatureKeyBedrockCCCompat: map[string]bool{"anthropic": true},
		},
	}
	require.True(t, c.IsBedrockCCCompatEnabled("anthropic"))
	require.False(t, c.IsBedrockCCCompatEnabled("openai"))
}

// TestGroupPolicy_IsBedrockCCCompatEnabled_MissingKey supplies a feature map
// containing an unrelated enabled feature. The compatibility lookup must remain
// disabled when its own feature key is absent.
func TestGroupPolicy_IsBedrockCCCompatEnabled_MissingKey(t *testing.T) {
	c := &testkit.Configuration{
		FeaturesConfig: map[string]any{
			"other_feature": true,
		},
	}
	require.False(t, c.IsBedrockCCCompatEnabled("bedrock"))
}
