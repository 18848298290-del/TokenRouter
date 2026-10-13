package capability

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Persisted protocol declarations can outlive the provider type that originally
// supplied them. These cases check authentication-based route rejection and
// demonstrate how one Chat entry chooses distinct native or converted targets
// for providers in a mixed group.

// TestAntigravityRetiredSnapshot supplies stale protocol declarations for
// retired Antigravity authentication types. Each route must fail, while OAuth
// routes for the same supplied sources resolve to GenerateContent.
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

// TestMixedGroupAutomaticAndExplicitRoutes sends Chat to Anthropic, OpenAI, and
// Gemini candidates. Automatic routing must select each candidate's usable
// target, and explicit empty fallbacks retain only OpenAI's native route.
func TestMixedGroupAutomaticAndExplicitRoutes(t *testing.T) {
	for _, tc := range []struct {
		platform string     // candidate provider platform name
		target   ProtocolID // expected usable upstream protocol
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
