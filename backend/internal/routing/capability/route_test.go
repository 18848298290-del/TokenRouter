package capability

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAntigravityRetiredSnapshot checks that stale declared protocols cannot restore an unsupported provider type.
func TestAntigravityRetiredSnapshot(t *testing.T) {
	protocols := []ProtocolID{ProtocolAnthropicMessages, ProtocolGeminiGenerateContent, ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses}
	for _, kind := range []string{ProviderTypeAPIKey, ProviderTypeUpstream, ProviderTypeSetupToken} {
		for _, source := range protocols {
			_, ok := ResolveRoute(ProviderProtocols{Platform: PlatformAntigravity, Type: kind, Enabled: protocols}, source, nil)
			require.False(t, ok, "%s %s", kind, source)
		}
		require.Empty(t, NativeProtocolOptions(PlatformAntigravity, kind, ""))
	}
	for _, source := range protocols {
		target, ok := ResolveRoute(ProviderProtocols{Platform: PlatformAntigravity, Type: ProviderTypeOAuth, Enabled: []ProtocolID{ProtocolGeminiGenerateContent}}, source, nil)
		require.True(t, ok)
		require.Equal(t, ProtocolGeminiGenerateContent, target)
	}
}

func TestMixedGroupAutomaticAndExplicitRoutes(t *testing.T) {
	// The same Chat request selects a route per provider, preferring an enabled native protocol.
	for _, tc := range []struct {
		platform string
		target   ProtocolID
	}{
		{PlatformAnthropic, ProtocolAnthropicMessages},
		{PlatformOpenAI, ProtocolOpenAIChatCompletions},
		{PlatformGemini, ProtocolGeminiGenerateContent},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			candidate := ProviderProtocols{Platform: tc.platform, Type: ProviderTypeAPIKey, Enabled: []ProtocolID{tc.target}}
			target, ok := ResolveRoute(candidate, ProtocolOpenAIChatCompletions, nil)
			require.True(t, ok)
			require.Equal(t, tc.target, target)
			_, ok = ResolveRoute(candidate, ProtocolOpenAIChatCompletions, map[ProtocolID][]ProtocolID{ProtocolOpenAIChatCompletions: {}})
			require.Equal(t, tc.platform == PlatformOpenAI, ok)
		})
	}
}
