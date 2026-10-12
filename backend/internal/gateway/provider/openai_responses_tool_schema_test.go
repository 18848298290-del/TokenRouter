package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestOpenAIResponsesToolSchemaCapabilities_PlatformBoundary(t *testing.T) {
	tests := []struct {
		platform         string
		repairNullType   bool
		removeLookaround bool
	}{
		{capability.PlatformOpenAI, true, true},
		{capability.PlatformAnthropic, true, false},
		{capability.PlatformKimi, true, false},
		{capability.PlatformZhipu, true, false},
		{capability.PlatformDeepseek, true, false},
		{capability.PlatformGrok, true, false},
		{capability.PlatformGemini, false, false},
		{capability.PlatformAntigravity, false, false},
		{capability.PlatformQoder, false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			require.Equal(t, tt.repairNullType, ShouldRepairOpenAIResponsesNullToolSchemaType(tt.platform))
			require.Equal(t, tt.removeLookaround, ShouldSanitizeOpenAIResponsesToolSchemaPatterns(tt.platform))
		})
	}
}

func TestSanitizeOpenAIResponsesToolSchemasForPlatform_ReplayBoundary(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","parameters":{"type":null,"properties":{"query":{"type":"string","pattern":"(?=keep)"}}}}]}`)

	// A malformed tool definition may be replayed after provider failover.
	// These compatible platforms repair parameter types and retain supported regex patterns.
	for _, platform := range []string{capability.PlatformAnthropic, capability.PlatformGrok, capability.PlatformKimi, capability.PlatformZhipu, capability.PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			for range 2 {
				normalized, changed, err := SanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
				require.NoError(t, err)
				require.True(t, changed)
				require.Equal(t, "object", gjson.GetBytes(normalized, "tools.0.parameters.type").String())
				require.Equal(t, "(?=keep)", gjson.GetBytes(normalized, "tools.0.parameters.properties.query.pattern").String())
			}
		})
	}

	openAI, changed, err := SanitizeOpenAIResponsesToolSchemasForPlatform(body, capability.PlatformOpenAI)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "object", gjson.GetBytes(openAI, "tools.0.parameters.type").String())
	require.False(t, gjson.GetBytes(openAI, "tools.0.parameters.properties.query.pattern").Exists())

	unsupported, changed, err := SanitizeOpenAIResponsesToolSchemasForPlatform(body, capability.PlatformGemini)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(unsupported))
}

func TestSanitizeOpenAIResponsesToolSchemasForPlatform_GrokObjectOnlyRootUnion(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"codex_app__automation_update","parameters":{"oneOf":[{"type":"object","properties":{"id":{"type":"string"}}},{"type":"object","properties":{}}]}}]}`)

	sanitized, changed, err := SanitizeOpenAIResponsesToolSchemasForPlatform(body, capability.PlatformGrok)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "object", gjson.GetBytes(sanitized, "tools.0.parameters.type").String())
	require.True(t, gjson.GetBytes(sanitized, "tools.0.parameters.oneOf").Exists())
}

func TestOpenAIResponsesToolSchemaPlatformGate_APIKeyAndOAuth(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","parameters":{"type":null,"pattern":"(?=drop)"}}]}`)
	for _, providerType := range []string{capability.ProviderTypeAPIKey, capability.ProviderTypeOAuth} {
		t.Run(providerType, func(t *testing.T) {
			normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{
				Platform: capability.PlatformOpenAI,
				Type:     providerType,
			}, false)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, "object", gjson.GetBytes(normalized, "tools.0.parameters.type").String())
			require.False(t, gjson.GetBytes(normalized, "tools.0.parameters.pattern").Exists())
		})
	}

	normalized, changed, err := NormalizeOpenAIResponsesWebSocketCompatibilityBody(body, &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeAPIKey,
	}, false)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(body), string(normalized))
}
