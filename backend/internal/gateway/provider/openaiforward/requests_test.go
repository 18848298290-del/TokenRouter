package openaiforward

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestBuildOpenAIResponsesURLForPlatform compares official and relay base URLs
// across DeepSeek, OpenAI, Zhipu, and Kimi. Each endpoint must retain the
// appropriate base prefix and Responses suffix without duplicating version
// paths.
func TestBuildOpenAIResponsesURLForPlatform(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://api.deepseek.com/responses", ResponsesEndpoint(capability.PlatformDeepseek, "https://api.deepseek.com"))
	require.Equal(t, "https://relay.example.com/responses", ResponsesEndpoint(capability.PlatformDeepseek, "https://relay.example.com"))
	require.Equal(t, "https://relay.example.com/v1/responses", ResponsesEndpoint(capability.PlatformDeepseek, "https://relay.example.com/v1"))
	require.Equal(t, "https://api.openai.com/v1/responses", ResponsesEndpoint(capability.PlatformOpenAI, "https://api.openai.com"))
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4/responses", ResponsesEndpoint(capability.PlatformZhipu, "https://open.bigmodel.cn/api/paas/v4"))
	require.Equal(t, "https://api.moonshot.cn/v1/responses", ResponsesEndpoint(capability.PlatformKimi, "https://api.moonshot.cn/v1"))
	require.Equal(t, "https://api.kimi.com/coding/v1/responses", ResponsesEndpoint(capability.PlatformKimi, "https://api.kimi.com/coding/v1"))
}

// TestNormalizeDeepSeekResponsesRequestBody selects explicit and adaptive
// native CN targets alongside Chat and OpenAI controls. Native requests must
// disable storage and remove continuation IDs, while the control routes retain
// their supplied bytes.
func TestNormalizeDeepSeekResponsesRequestBody(t *testing.T) {
	t.Parallel()

	deepseekResponses := &providercore.Record{
		Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": providercore.APIProtocolResponses},
	}
	body := []byte(`{"model":"deepseek-v4-pro","store":true,"previous_response_id":"resp_123","input":"hi"}`)
	normalized := NormalizeCNResponsesBody((providercore.ProtocolTarget{Record: deepseekResponses}).UsesNativeCNResponses(), body)
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
	require.False(t, gjson.GetBytes(normalized, "previous_response_id").Exists())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(normalized, "model").String())

	deepseekAdaptive := &providercore.Record{
		Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": providercore.APIProtocolAdaptive},
	}
	adaptiveNormalized := NormalizeCNResponsesBody((providercore.ProtocolTarget{Record: deepseekAdaptive}).UsesNativeCNResponses(), body)
	require.False(t, gjson.GetBytes(adaptiveNormalized, "store").Bool())
	require.False(t, gjson.GetBytes(adaptiveNormalized, "previous_response_id").Exists())

	// A Chat-resolved provider supplies no native-CN marker, so its continuation
	// and storage fields stay available for that route's separate preparation.
	deepseekCC := &providercore.Record{Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey}
	require.Equal(t, string(body), string(NormalizeCNResponsesBody((providercore.ProtocolTarget{Record: deepseekCC}).UsesNativeCNResponses(), body)))

	kimiResponses := &providercore.Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": providercore.APIProtocolResponses},
	}
	kimiNormalized := NormalizeCNResponsesBody((providercore.ProtocolTarget{Record: kimiResponses}).UsesNativeCNResponses(), body)
	require.False(t, gjson.GetBytes(kimiNormalized, "store").Bool())
	require.False(t, gjson.GetBytes(kimiNormalized, "previous_response_id").Exists())

	kimiCodingAdaptive := &providercore.Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": providercore.APIProtocolAdaptive, "provider_mode": providercore.ProviderModeCoding},
	}
	kimiCodingNormalized := NormalizeCNResponsesBody((providercore.ProtocolTarget{Record: kimiCodingAdaptive}).UsesNativeCNResponses(), body)
	require.False(t, gjson.GetBytes(kimiCodingNormalized, "store").Bool())
	require.False(t, gjson.GetBytes(kimiCodingNormalized, "previous_response_id").Exists())

	// OpenAI retains server-side Responses state because this normalization is
	// scoped to native CN targets rather than all Responses-compatible providers.
	openai := &providercore.Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	require.Equal(t, string(body), string(NormalizeCNResponsesBody((providercore.ProtocolTarget{Record: openai}).UsesNativeCNResponses(), body)))
}
