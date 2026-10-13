package routing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Reasoning configuration distinguishes ranked ceilings from mapping choices,
// including none and platform-specific values. These fixtures check canonical
// labels, scope normalization, duplicate source-and-model rejection, and
// over-limit action defaults while preserving the accepted Anthropic subset
// separately from OpenAI.

// TestNormalizeMaxReasoningEffort supplies empty, separator-bearing, max, none,
// and unknown labels. Only ranked values must normalize, keeping max distinct
// while empty, none, and invalid ceilings return empty.
func TestNormalizeMaxReasoningEffort(t *testing.T) {
	tests := []struct {
		name string // subtest case display name
		in   string // raw group ceiling label
		want string // expected normalized ceiling label
	}{
		{name: "empty", in: "", want: ""},
		{name: "separator", in: "x-high", want: "xhigh"},
		{name: "max is distinct", in: "max", want: "max"},
		{name: "none is unsupported", in: "none", want: ""},
		{name: "invalid", in: "banana", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeMaxReasoningEffort(tt.in))
		})
	}
}

// TestNormalizeReasoningEffortMappings supplies global and model-scoped rules
// across declared platform sets. Normalization must preserve valid scope
// distinctions, reject duplicate or invalid values, and accept the tested
// Anthropic subset.
func TestNormalizeReasoningEffortMappings(t *testing.T) {
	t.Run("canonicalizes fixed OpenAI values", func(t *testing.T) {
		got, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: " MAX ", To: " x-high "},
			{From: "minimal", To: "high"},
			{From: "none", To: "low"},
		})
		require.NoError(t, err)
		require.Equal(t, []ReasoningEffortMapping{
			{From: "max", To: "xhigh"},
			{From: "minimal", To: "high"},
			{From: "none", To: "low"},
		}, got)
	})

	t.Run("rejects empty values", func(t *testing.T) {
		_, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{{From: "max"}})
		require.ErrorContains(t, err, "empty or unknown")
	})

	t.Run("rejects duplicate sources case insensitively", func(t *testing.T) {
		_, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "xhigh"},
			{From: " MAX ", To: "high"},
		})
		require.ErrorContains(t, err, "duplicate")
	})

	t.Run("allows same source across different model scopes", func(t *testing.T) {
		got, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: "prefix", Model: " gpt "},
			{From: "max", To: "medium", MatchType: "exact", Model: "GPT-5.4"},
			{From: "max", To: "high"},
		})
		require.NoError(t, err)
		require.Equal(t, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: ReasoningEffortMatchPrefix, Model: "gpt"},
			{From: "max", To: "medium", MatchType: ReasoningEffortMatchExact, Model: "GPT-5.4"},
			{From: "max", To: "high"},
		}, got)
	})

	t.Run("defaults missing match type to exact when model is set", func(t *testing.T) {
		got, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "low", Model: "gpt-5.4"},
		})
		require.NoError(t, err)
		require.Equal(t, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: ReasoningEffortMatchExact, Model: "gpt-5.4"},
		}, got)
	})

	t.Run("empty type and model collapse to a global mapping", func(t *testing.T) {
		got, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: "prefix"},
			{From: "high", To: "low", MatchType: "suffix"},
		})
		require.NoError(t, err)
		require.Equal(t, []ReasoningEffortMapping{
			{From: "max", To: "low"},
			{From: "high", To: "low"},
		}, got)
	})

	t.Run("canonicalizes suffix match", func(t *testing.T) {
		got, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: " SUFFIX ", Model: " mini "},
		})
		require.NoError(t, err)
		require.Equal(t, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: ReasoningEffortMatchSuffix, Model: "mini"},
		}, got)
	})

	t.Run("rejects invalid match type", func(t *testing.T) {
		_, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: "wildcard", Model: "gpt"},
		})
		require.ErrorContains(t, err, "invalid match_type")
	})

	t.Run("rejects duplicate source within the same model scope", func(t *testing.T) {
		_, err := NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{
			{From: "max", To: "low", MatchType: "prefix", Model: "gpt"},
			{From: "MAX", To: "high", MatchType: "PREFIX", Model: " GPT "},
		})
		require.ErrorContains(t, err, "duplicate")
		require.ErrorContains(t, err, "gpt")
	})

	t.Run("rejects mappings for non OpenAI platforms", func(t *testing.T) {
		for _, platform := range []string{capability.PlatformGemini, capability.PlatformAntigravity, capability.PlatformGrok} {
			_, err := NormalizeReasoningEffortMappings(platform, []ReasoningEffortMapping{{From: "low", To: "high"}})
			require.ErrorContains(t, err, "only supported for platforms")
		}

		got, err := NormalizeReasoningEffortMappings(capability.PlatformAnthropic, []ReasoningEffortMapping{{From: " MAX ", To: " x-high "}})
		require.NoError(t, err)
		require.Equal(t, []ReasoningEffortMapping{{From: "max", To: "xhigh"}}, got)
		_, err = NormalizeReasoningEffortMappings(capability.PlatformAnthropic, []ReasoningEffortMapping{{From: "minimal", To: "low"}})
		require.ErrorContains(t, err, "not supported for platform \"anthropic\"")

		_, err = NormalizeReasoningEffortMappings(capability.PlatformOpenAI, []ReasoningEffortMapping{{From: "ultra", To: "high"}})
		require.ErrorContains(t, err, "empty or unknown")
	})
}

// TestNormalizeMaxReasoningEffortForPlatform checks accepted OpenAI and
// Anthropic ceiling values with unsupported platform and none cases. Supported
// labels must remain canonical, and invalid populated configurations must
// return errors.
func TestNormalizeMaxReasoningEffortForPlatform(t *testing.T) {
	value, err := NormalizeMaxReasoningEffortForPlatform(capability.PlatformOpenAI, "max")
	require.NoError(t, err)
	require.Equal(t, "max", value)
	value, err = NormalizeMaxReasoningEffortForPlatform(capability.PlatformAnthropic, "xhigh")
	require.NoError(t, err)
	require.Equal(t, "xhigh", value)

	for _, platform := range []string{capability.PlatformGemini, capability.PlatformAntigravity, capability.PlatformGrok} {
		_, err = NormalizeMaxReasoningEffortForPlatform(platform, "low")
		require.ErrorContains(t, err, "only supported for platforms")
	}

	_, err = NormalizeMaxReasoningEffortForPlatform(capability.PlatformOpenAI, "none")
	require.ErrorContains(t, err, "not supported")
}

// TestNormalizeMaxReasoningEffortOverLimit supplies blank, padded downgrade,
// uppercase deny, and unknown action text. Blank and supported values must
// normalize to their selected action while unknown input returns empty.
func TestNormalizeMaxReasoningEffortOverLimit(t *testing.T) {
	require.Equal(t, ReasoningEffortOverLimitDowngrade, NormalizeMaxReasoningEffortOverLimit(""))
	require.Equal(t, ReasoningEffortOverLimitDowngrade, NormalizeMaxReasoningEffortOverLimit(" downgrade "))
	require.Equal(t, ReasoningEffortOverLimitDeny, NormalizeMaxReasoningEffortOverLimit("DENY"))
	require.Empty(t, NormalizeMaxReasoningEffortOverLimit("block"))
}

// TestNormalizeMaxReasoningEffortOverLimitForPlatform checks default downgrade
// and accepted Anthropic deny configuration. Both supported actions must
// normalize, while the supplied unknown action returns its validation error.
func TestNormalizeMaxReasoningEffortOverLimitForPlatform(t *testing.T) {
	value, err := NormalizeMaxReasoningEffortOverLimitForPlatform(capability.PlatformOpenAI, "")
	require.NoError(t, err)
	require.Equal(t, ReasoningEffortOverLimitDowngrade, value)

	value, err = NormalizeMaxReasoningEffortOverLimitForPlatform(capability.PlatformAnthropic, "")
	require.NoError(t, err)
	require.Equal(t, ReasoningEffortOverLimitDowngrade, value)

	value, err = NormalizeMaxReasoningEffortOverLimitForPlatform(capability.PlatformAnthropic, "deny")
	require.NoError(t, err)
	require.Equal(t, ReasoningEffortOverLimitDeny, value)
	_, err = NormalizeMaxReasoningEffortOverLimitForPlatform(capability.PlatformOpenAI, "block")
	require.ErrorContains(t, err, "not supported")
}
