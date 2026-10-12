package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAI compatibility settings must retain the difference between omitted
// workloads and an explicit empty list. These cases also cover legacy edit
// conversion, continuation booleans and rejection of a current route value
// that the legacy format tolerated.

// TestNormalizeOpenAIAPIKeyConfigurationDefaultsAndExplicitEmpty compares
// missing API-key settings with an explicit empty legacy workload list.
// Defaulted workloads and route mode are installed for omission, while an
// empty selection disables the checked workload capabilities.
func TestNormalizeOpenAIAPIKeyConfigurationDefaultsAndExplicitEmpty(t *testing.T) {
	t.Run("缺失配置写入显式默认值", func(t *testing.T) {
		provider := &Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}

		require.NoError(t, NormalizeOpenAIAPIKeyConfiguration(provider))
		require.Equal(t, []string{"text_generation", "embeddings"}, provider.Credentials[OpenAIWorkloadCapabilitiesCredentialKey])
		require.Equal(t, "preserve_client_protocol", provider.Extra[ExtraKeyTextRouteMode])
		require.NotContains(t, provider.Extra, "openai_responses_probe_status")
		require.Equal(t, false, provider.Extra[ExtraKeyResponsesContinuationSupported])
	})

	t.Run("显式空能力集合保持为空", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Credentials: map[string]any{
				LegacyOpenAICapabilitiesCredentialKey: []any{},
			},
		}

		require.NoError(t, NormalizeOpenAIAPIKeyConfiguration(provider))
		require.Equal(t, []string{}, provider.Credentials[OpenAIWorkloadCapabilitiesCredentialKey])
		require.False(t, provider.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityTextGeneration, nil))
		require.False(t, provider.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityEmbeddings, nil))
	})
}

// TestNormalizeOpenAIAPIKeyConfigurationPatchSupportsLegacyBulkPayload
// supplies legacy workload and route settings alongside an obsolete probe
// flag. Normalization writes current representations and removes legacy keys
// and the discarded probe field.
func TestNormalizeOpenAIAPIKeyConfigurationPatchSupportsLegacyBulkPayload(t *testing.T) {
	credentials := map[string]any{
		LegacyOpenAICapabilitiesCredentialKey: []any{"chat_completions", "embeddings"},
	}
	extra := map[string]any{
		LegacyOpenAIResponsesModeExtraKey: "auto",
		"openai_responses_supported":      true,
	}

	require.NoError(t, NormalizeOpenAIAPIKeyConfigurationPatch(credentials, extra))
	require.Equal(t, []string{"text_generation", "embeddings"}, credentials[OpenAIWorkloadCapabilitiesCredentialKey])
	require.Equal(t, "preserve_client_protocol", extra[ExtraKeyTextRouteMode])
	require.NotContains(t, extra, "openai_responses_probe_status")
	require.NotContains(t, credentials, LegacyOpenAICapabilitiesCredentialKey)
	require.NotContains(t, extra, LegacyOpenAIResponsesModeExtraKey)
	require.NotContains(t, extra, "openai_responses_supported")
}

// TestNormalizeOpenAIResponsesContinuationSupported supplies true, false, null
// and a string continuation value through patch normalization. Booleans remain
// unchanged, null becomes false, and the string is rejected with the
// continuation validation error.
func TestNormalizeOpenAIResponsesContinuationSupported(t *testing.T) {
	t.Run("explicit values are preserved", func(t *testing.T) {
		extra := map[string]any{
			ExtraKeyResponsesContinuationSupported: true,
		}
		require.NoError(t, NormalizeOpenAIAPIKeyConfigurationPatch(nil, extra))
		require.Equal(t, true, extra[ExtraKeyResponsesContinuationSupported])

		extra[ExtraKeyResponsesContinuationSupported] = false
		require.NoError(t, NormalizeOpenAIAPIKeyConfigurationPatch(nil, extra))
		require.Equal(t, false, extra[ExtraKeyResponsesContinuationSupported])
	})

	t.Run("null becomes false", func(t *testing.T) {
		extra := map[string]any{
			ExtraKeyResponsesContinuationSupported: nil,
		}
		require.NoError(t, NormalizeOpenAIAPIKeyConfigurationPatch(nil, extra))
		require.Equal(t, false, extra[ExtraKeyResponsesContinuationSupported])
	})

	t.Run("invalid type is rejected", func(t *testing.T) {
		extra := map[string]any{
			ExtraKeyResponsesContinuationSupported: "true",
		}
		err := NormalizeOpenAIAPIKeyConfigurationPatch(nil, extra)
		require.Error(t, err)
		require.Contains(t, err.Error(), "OPENAI_RESPONSES_CONTINUATION_INVALID")
	})
}

// TestNormalizeOpenAITextRouteModeRejectsInvalidNewValue supplies the legacy
// auto spelling under the current route key. Patch normalization returns an
// error, protecting exact current-field validation rather than legacy
// tolerance.
func TestNormalizeOpenAITextRouteModeRejectsInvalidNewValue(t *testing.T) {
	extra := map[string]any{ExtraKeyTextRouteMode: "auto"}

	err := NormalizeOpenAIAPIKeyConfigurationPatch(nil, extra)

	require.Error(t, err)
}
