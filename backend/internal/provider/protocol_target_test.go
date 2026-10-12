package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestGetAPIProtocol supplies supported, absent, and invalid legacy variants
// across CN platforms. It checks recognized routing modes, Zhipu's Responses
// fallback, and default Chat selection on an OpenAI record.
func TestGetAPIProtocol(t *testing.T) {
	t.Parallel()

	mk := func(platform, protocol string) ProtocolTarget {
		creds := map[string]any{"api_key": "sk-test"}
		if protocol != "" {
			creds["api_protocol"] = protocol
		}
		return ProtocolTarget{Record: &Record{Platform: platform, Type: capability.ProviderTypeAPIKey, Credentials: creds}}
	}

	require.Equal(t, APIProtocolChatCompletions, mk(capability.PlatformKimi, "").GetAPIProtocol(), "缺失回退默认")
	require.Equal(t, APIProtocolAnthropic, mk(capability.PlatformZhipu, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolAnthropic, mk(capability.PlatformKimi, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolAnthropic, mk(capability.PlatformDeepseek, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolResponses, mk(capability.PlatformDeepseek, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolResponses, mk(capability.PlatformKimi, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolAdaptive, mk(capability.PlatformKimi, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolAdaptive, mk(capability.PlatformZhipu, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolAdaptive, mk(capability.PlatformDeepseek, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolChatCompletions, mk(capability.PlatformZhipu, APIProtocolResponses).GetAPIProtocol(), "zhipu 无 responses 端点")
	require.Equal(t, APIProtocolChatCompletions, mk(capability.PlatformKimi, "bogus").GetAPIProtocol(), "非法值回退默认")
	require.Equal(t, APIProtocolChatCompletions, (ProtocolTarget{Record: &Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}}).GetAPIProtocol(), "非 CN 供应商恒为默认")
}

// TestGetCodingPlanProviderUsesPlatformInsteadOfURL supplies coding-plan records
// with custom or another platform's endpoint spelling. It checks that Kimi and
// Zhipu identity comes from Platform rather than their configured URL.
func TestGetCodingPlanProviderUsesPlatformInsteadOfURL(t *testing.T) {
	t.Parallel()
	kimi := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"provider_mode": ProviderModeCoding,
			"base_url":      "https://relay.example/custom",
		},
	}}
	require.Equal(t, capability.PlatformKimi, kimi.GetCodingPlanProvider())
	zhipu := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"provider_mode": ProviderModeCoding,
			"base_url":      "https://api.kimi.com/coding/v1",
		},
	}}
	require.Equal(t, capability.PlatformZhipu, zhipu.GetCodingPlanProvider(), "中继路径不得改变平台身份")
}

// TestSupportsNativeCNResponses compares DeepSeek, Kimi, Zhipu, and OpenAI
// platform records, including Kimi coding mode. It checks that native Responses
// support follows platform identity for those supplied configurations.
func TestSupportsNativeCNResponses(t *testing.T) {
	t.Parallel()
	require.True(t, (ProtocolTarget{Record: &Record{Platform: capability.PlatformDeepseek}}).SupportsNativeCNResponses())
	require.True(t, (ProtocolTarget{Record: &Record{Platform: capability.PlatformKimi}}).SupportsNativeCNResponses())
	require.True(t, (ProtocolTarget{Record: &Record{Platform: capability.PlatformKimi, Credentials: map[string]any{"provider_mode": ProviderModeCoding}}}).SupportsNativeCNResponses())
	require.False(t, (ProtocolTarget{Record: &Record{Platform: capability.PlatformZhipu}}).SupportsNativeCNResponses())
	require.False(t, (ProtocolTarget{Record: &Record{Platform: capability.PlatformOpenAI}}).SupportsNativeCNResponses())
}

// TestAdaptiveProtocolBaseURLs combines adaptive selection with each supported
// CN platform and provider mode. It checks their default Chat, Messages, and
// Responses addresses plus the Messages endpoint convenience method.
func TestAdaptiveProtocolBaseURLs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string // endpoint scenario display name
		platform      string // configured CN platform identity
		mode          string // configured provider billing mode
		wantChat      string // expected default Chat address
		wantAnthropic string // expected default Messages address
		wantResponses string // expected default Responses address
	}{
		{"kimi payg", capability.PlatformKimi, ProviderModePayG, DefaultKimiPayGBaseURL, DefaultKimiPayGAnthropicBaseURL, DefaultKimiPayGBaseURL},
		{"kimi coding", capability.PlatformKimi, ProviderModeCoding, DefaultKimiCodingBaseURL, DefaultKimiCodingAnthropicBaseURL, DefaultKimiCodingBaseURL},
		{"zhipu payg", capability.PlatformZhipu, ProviderModePayG, DefaultZhipuPayGBaseURL, DefaultZhipuAnthropicBaseURL, DefaultZhipuPayGBaseURL},
		{"zhipu coding", capability.PlatformZhipu, ProviderModeCoding, DefaultZhipuCodingBaseURL, DefaultZhipuAnthropicBaseURL, DefaultZhipuCodingBaseURL},
		{"deepseek", capability.PlatformDeepseek, ProviderModePayG, DefaultDeepseekBaseURL, DefaultDeepseekAnthropicBaseURL, DefaultDeepseekBaseURL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := ProtocolTarget{Record: &Record{Platform: tc.platform, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{
				"api_protocol":  APIProtocolAdaptive,
				"provider_mode": tc.mode,
			}}}
			require.Equal(t, tc.wantChat, provider.GetCNProtocolBaseURL(APIProtocolChatCompletions))
			require.Equal(t, tc.wantAnthropic, provider.GetCNProtocolBaseURL(APIProtocolAnthropic))
			require.Equal(t, tc.wantResponses, provider.GetCNProtocolBaseURL(APIProtocolResponses))
			require.Equal(t, tc.wantAnthropic, provider.GetAnthropicProtocolBaseURL())
		})
	}
}

// TestAdaptiveProtocolBaseURLOverrides supplies three distinct protocol slots
// alongside a legacy Chat base on a DeepSeek record. It checks that adaptive
// lookup uses the corresponding slot for every asserted endpoint.
func TestAdaptiveProtocolBaseURLOverrides(t *testing.T) {
	t.Parallel()

	provider := ProtocolTarget{Record: &Record{Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{
		"api_protocol": APIProtocolAdaptive,
		"base_url":     "https://legacy-chat.example.com",
		"api_base_urls": map[string]any{
			APIProtocolChatCompletions: "https://chat.example.com",
			APIProtocolAnthropic:       "https://anthropic.example.com",
			APIProtocolResponses:       "https://responses.example.com",
		},
	}}}

	require.Equal(t, "https://chat.example.com", provider.GetOpenAIBaseURL())
	require.Equal(t, "https://chat.example.com", provider.GetCNProtocolBaseURL(APIProtocolChatCompletions))
	require.Equal(t, "https://anthropic.example.com", provider.GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://responses.example.com", provider.GetCNProtocolBaseURL(APIProtocolResponses))
}

// TestAnthropicProtocolBaseURL supplies fixed legacy Messages records with
// platform defaults and a custom relay. It checks the selected Messages bases
// and an empty result for a fixed Chat configuration.
func TestAnthropicProtocolBaseURL(t *testing.T) {
	t.Parallel()

	// Default endpoints depend on platform and provider mode.
	require.Equal(t, "https://api.moonshot.cn/anthropic", (ProtocolTarget{Record: &Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}}).GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://api.kimi.com/coding", (ProtocolTarget{Record: &Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic, "provider_mode": ProviderModeCoding},
	}}).GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://open.bigmodel.cn/api/anthropic", (ProtocolTarget{Record: &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}}).GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://api.deepseek.com/anthropic", (ProtocolTarget{Record: &Record{
		Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}}).GetAnthropicProtocolBaseURL())

	// An explicit base_url overrides the legacy default.
	require.Equal(t, "https://custom.example.com/anthropic", (ProtocolTarget{Record: &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic, "base_url": "https://custom.example.com/anthropic"},
	}}).GetAnthropicProtocolBaseURL())

	// A fixed Chat configuration has no Messages base URL.
	require.Empty(t, (ProtocolTarget{Record: &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://open.bigmodel.cn/api/paas/v4"},
	}}).GetAnthropicProtocolBaseURL())
}

// TestGetOpenAIFormatBaseURL_ProtocolAware supplies built-in and custom legacy
// Messages bases plus a Chat record. It checks platform Chat defaults, terminal
// Messages-path removal on relays, and direct use of the supplied Chat base.
func TestGetOpenAIFormatBaseURL_ProtocolAware(t *testing.T) {
	t.Parallel()

	zhipuAnthropic := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAnthropic,
			"base_url":     "https://open.bigmodel.cn/api/anthropic",
		},
	}}
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4", zhipuAnthropic.GetOpenAIFormatBaseURL())

	kimiCodingAnthropic := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol":  APIProtocolAnthropic,
			"provider_mode": ProviderModeCoding,
			"base_url":      "https://api.kimi.com/coding",
		},
	}}
	require.Equal(t, "https://api.kimi.com/coding/v1", kimiCodingAnthropic.GetOpenAIFormatBaseURL())

	deepseekRelay := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAnthropic,
			"base_url":     "https://relay.example.com/proxy/deepseek/anthropic/",
		},
	}}
	require.Equal(t, "https://relay.example.com/proxy/deepseek", deepseekRelay.GetOpenAIFormatBaseURL())

	kimiRelay := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAnthropic,
			"base_url":     "https://relay.example.com/moonshot/anthropic",
		},
	}}
	require.Equal(t, "https://relay.example.com/moonshot", kimiRelay.GetOpenAIFormatBaseURL())

	// Chat configurations use the configured base_url directly.
	ccProvider := ProtocolTarget{Record: &Record{
		Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ds-relay.example.com"},
	}}
	require.Equal(t, "https://ds-relay.example.com", ccProvider.GetOpenAIFormatBaseURL())
}
