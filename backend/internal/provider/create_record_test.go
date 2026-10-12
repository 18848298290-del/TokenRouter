package provider

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestBuildProviderForCreateRequiresCustomGeminiThirdPartyBaseURL(t *testing.T) {
	invalidInput := &CreateProviderInput{
		Name:     "third-party Gemini",
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			GeminiProviderTypeCredentialKey: GeminiProviderTypeThirdParty,
			"base_url":                      "https://generativelanguage.googleapis.com",
		},
	}

	_, err := BuildProviderForCreate(invalidInput, nil, CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString})
	require.Error(t, err)
	require.Equal(t, "GEMINI_THIRD_PARTY_BASE_URL_REQUIRED", apperror.Reason(err))

	validInput := &CreateProviderInput{
		Name:     "third-party Gemini",
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			GeminiProviderTypeCredentialKey: GeminiProviderTypeThirdParty,
			"base_url":                      "https://provider.example.test",
		},
	}
	provider, err := BuildProviderForCreate(validInput, nil, CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString})
	require.NoError(t, err)
	require.NotNil(t, provider)
}

// TestBuildProviderForCreateNormalizesLegacyOpenAIConfigurationForCreateAndImport checks deprecated OpenAI settings in the shared creation path.
func TestBuildProviderForCreateNormalizesLegacyOpenAIConfigurationForCreateAndImport(t *testing.T) {
	input := &CreateProviderInput{
		Name:     "legacy-openai",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "secret",
			"openai_capabilities": map[string]any{
				"chat_completions": true,
				"embeddings":       false,
			},
		},
	}
	extra := map[string]any{
		"openai_responses_mode":      "force_responses",
		"openai_responses_supported": false,
		"unrelated":                  map[string]any{"keep": true},
	}

	provider, err := BuildProviderForCreate(input, extra, CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString})

	require.NoError(t, err)
	require.Contains(t, provider.UpstreamProtocols(), protocol.ProtocolOpenAIResponses)
	require.NotContains(t, provider.UpstreamProtocols(), protocol.ProtocolOpenAIChatCompletions)
	require.NotContains(t, provider.Extra, ExtraKeyTextRouteMode)
	require.NotContains(t, provider.Extra, "openai_responses_probe_status")
	require.Equal(t, false, provider.Extra[ExtraKeyResponsesContinuationSupported])
	require.Equal(t, map[string]any{"keep": true}, provider.Extra["unrelated"])
	require.NotContains(t, provider.Credentials, LegacyOpenAICapabilitiesCredentialKey)
	require.NotContains(t, provider.Extra, LegacyOpenAIResponsesModeExtraKey)
	require.NotContains(t, provider.Extra, "openai_responses_supported")
}
