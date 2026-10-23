package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// TestImageStreamModeDefaultsAndValidation checks older records and independent
// endpoint selections. Invalid stored values and incompatible provider types
// must fail the shared save normalization used by every management write.
func TestImageStreamModeDefaultsAndValidation(t *testing.T) {
	value := &Record{Platform: PlatformOpenAI, Type: ProviderTypeAPIKey, Credentials: map[string]any{}}
	mode, err := value.ImageStreamMode(protocol.ProtocolImagesGenerations)
	require.NoError(t, err)
	require.Equal(t, protocol.ImageModeBoth, mode)
	for _, key := range []string{ImagesGenerationsModeKey, ImagesEditsModeKey} {
		for _, raw := range []any{"both", "streaming", "non_streaming", "", "auto", nil, true, []string{"streaming"}} {
			t.Run(key, func(t *testing.T) {
				candidate := &Record{Platform: PlatformOpenAI, Type: ProviderTypeAPIKey, Credentials: map[string]any{key: raw, UpstreamProtocolsKey: []string{}}}
				err := NormalizeProviderProtocols(candidate)
				text, ok := raw.(string)
				valid := ok && protocol.ImageStreamMode(text).Valid()
				if valid {
					require.NoError(t, err)
					require.Empty(t, candidate.UpstreamProtocols(), "mode settings cannot enable a protocol")
				} else {
					require.ErrorContains(t, err, key)
				}
			})
		}
	}
	value.Credentials = map[string]any{ImagesGenerationsModeKey: "non_streaming", ImagesEditsModeKey: "streaming"}
	generation, err := value.ImageStreamMode(protocol.ProtocolImagesGenerations)
	require.NoError(t, err)
	edit, err := value.ImageStreamMode(protocol.ProtocolImagesEdits)
	require.NoError(t, err)
	require.False(t, generation.UpstreamStream(true))
	require.True(t, edit.UpstreamStream(false))
	value.Type = ProviderTypeOAuth
	require.ErrorContains(t, NormalizeProviderProtocols(value), "OpenAI API key")
	value.Type = ProviderTypeAPIKey
	value.Platform = PlatformGrok
	require.ErrorContains(t, NormalizeProviderProtocols(value), "OpenAI API key")
}
