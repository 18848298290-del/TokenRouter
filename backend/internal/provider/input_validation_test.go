package provider

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

func TestCNProviderProviderModeAndCredentialValidation(t *testing.T) {
	t.Parallel()
	historical := &Record{
		Platform:    capability.PlatformKimi,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}
	require.Equal(t, ProviderModePayG, historical.GetProviderMode())
	require.Equal(t, APIProtocolChatCompletions, (ProtocolTarget{Record: historical}).GetAPIProtocol())
	require.NoError(t, NormalizeCNProviderCredentials(historical, false))
	require.NotContains(t, historical.Credentials, "provider_mode", "编辑历史提供商不应强制回写默认字段")

	created := &Record{
		Platform:    capability.PlatformZhipu,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	}
	require.NoError(t, NormalizeCNProviderCredentials(created, true))
	require.Equal(t, ProviderModePayG, created.Credentials["provider_mode"])
	require.Equal(t, APIProtocolChatCompletions, created.Credentials["api_protocol"])

	invalidResponses := &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolResponses},
	}
	require.Error(t, NormalizeCNProviderCredentials(invalidResponses, false))
	invalidType := &Record{Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeOAuth}
	require.Error(t, NormalizeCNProviderCredentials(invalidType, false))
}

func TestNormalizeProviderConcurrencyDefaultsInvalidGrokOAuthToOne(t *testing.T) {
	require.Equal(t, 1, NormalizeProviderConcurrency(capability.PlatformGrok, capability.ProviderTypeOAuth, 0))
	require.Equal(t, 1, NormalizeProviderConcurrency(capability.PlatformGrok, capability.ProviderTypeOAuth, -5))
}

func TestNormalizeProviderConcurrencyPreservesExplicitValues(t *testing.T) {
	require.Equal(t, 50, NormalizeProviderConcurrency(capability.PlatformGrok, capability.ProviderTypeOAuth, 50))
	require.Equal(t, 2, NormalizeProviderConcurrency(capability.PlatformOpenAI, capability.ProviderTypeOAuth, 2))
	require.Equal(t, 2, NormalizeProviderConcurrency(capability.PlatformGrok, capability.ProviderTypeAPIKey, 2))
}

// cnProviderTestCredentials supplies custom endpoints to check that saving retains the requested URLs.
func cnProviderTestCredentials(platform, mode, protocol string) map[string]any {
	credentials := map[string]any{
		"api_key":       "sk-test",
		"provider_mode": mode,
		"api_protocol":  protocol,
		"base_url":      "https://relay.example.test/v1",
	}
	if protocol == APIProtocolAdaptive {
		urls := map[string]any{
			APIProtocolChatCompletions: "https://relay.example.test/v1",
			APIProtocolAnthropic:       "https://relay.example.test/anthropic",
		}
		if platform != capability.PlatformZhipu {
			urls[APIProtocolResponses] = "https://relay.example.test/responses"
		}
		credentials["api_base_urls"] = urls
	}
	return credentials
}

// TestCNProviderCredentialValidationRejectsInvalid checks structured errors for unsupported credential combinations.
func TestCNProviderCredentialValidationRejectsInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, platform, providerType, mode, protocol, reason string
	}{
		{"智谱原生 Responses", capability.PlatformZhipu, capability.ProviderTypeAPIKey, ProviderModePayG, APIProtocolResponses, "CN_PROVIDER_PROTOCOL_INVALID"},
		{"智谱 Coding 原生 Responses", capability.PlatformZhipu, capability.ProviderTypeAPIKey, ProviderModeCoding, APIProtocolResponses, "CN_PROVIDER_PROTOCOL_INVALID"},
		{"DeepSeek Coding", capability.PlatformDeepseek, capability.ProviderTypeAPIKey, ProviderModeCoding, APIProtocolAdaptive, "CN_PROVIDER_MODE_INVALID"},
		{"非 API Key", capability.PlatformDeepseek, capability.ProviderTypeOAuth, ProviderModePayG, APIProtocolAdaptive, "CN_PROVIDER_PROVIDER_TYPE_INVALID"},
		{"未知模式", capability.PlatformKimi, capability.ProviderTypeAPIKey, "unknown", APIProtocolAdaptive, "CN_PROVIDER_PROVIDER_MODE_INVALID"},
		{"未知协议", capability.PlatformKimi, capability.ProviderTypeAPIKey, ProviderModePayG, "unknown", "CN_PROVIDER_PROTOCOL_INVALID"},
	}
	for _, tc := range cases {
		for _, create := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/create=%t", tc.name, create), func(t *testing.T) {
				provider := &Record{
					Platform: tc.platform, Type: tc.providerType,
					Credentials: cnProviderTestCredentials(tc.platform, tc.mode, tc.protocol),
				}
				err := NormalizeCNProviderCredentials(provider, create)
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
				require.Equal(t, tc.reason, apperror.Reason(err))
			})
		}
	}
}

func TestNormalizeGrokMediaEligibilityExtra(t *testing.T) {
	t.Run("boolean override is accepted", func(t *testing.T) {
		extra, err := NormalizeGrokMediaEligibilityExtra(capability.PlatformGrok, map[string]any{GrokMediaEligibleExtraKey: false})

		require.NoError(t, err)
		require.Equal(t, false, extra[GrokMediaEligibleExtraKey])
	})

	t.Run("null clears override", func(t *testing.T) {
		extra, err := NormalizeGrokMediaEligibilityExtra(capability.PlatformGrok, map[string]any{GrokMediaEligibleExtraKey: nil})

		require.NoError(t, err)
		require.NotContains(t, extra, GrokMediaEligibleExtraKey)
	})

	t.Run("malformed override is rejected", func(t *testing.T) {
		_, err := NormalizeGrokMediaEligibilityExtra(capability.PlatformGrok, map[string]any{GrokMediaEligibleExtraKey: "false"})

		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	})

	t.Run("other platforms ignore provider owned value", func(t *testing.T) {
		extra := map[string]any{GrokMediaEligibleExtraKey: "provider-owned"}
		normalized, err := NormalizeGrokMediaEligibilityExtra(capability.PlatformOpenAI, extra)

		require.NoError(t, err)
		require.Equal(t, extra, normalized)
	})
}

func TestNormalizeGrokMediaEligibilityUpdateExtra(t *testing.T) {
	provider := &Record{Platform: capability.PlatformGrok, Extra: map[string]any{GrokMediaEligibleExtraKey: false}}

	t.Run("omitted override preserves current value", func(t *testing.T) {
		input := &UpdateProviderInput{Extra: map[string]any{"quota_used": float64(1)}}
		normalized, err := NormalizeGrokMediaEligibilityUpdateExtra(provider, input, map[string]any{"quota_used": float64(1)})

		require.NoError(t, err)
		require.Equal(t, false, normalized[GrokMediaEligibleExtraKey])
	})

	t.Run("null removes current override", func(t *testing.T) {
		input := &UpdateProviderInput{Extra: map[string]any{GrokMediaEligibleExtraKey: nil}}
		normalized, err := NormalizeGrokMediaEligibilityUpdateExtra(provider, input, map[string]any{GrokMediaEligibleExtraKey: nil})

		require.NoError(t, err)
		require.NotContains(t, normalized, GrokMediaEligibleExtraKey)
		require.Contains(t, input.Extra, GrokMediaEligibleExtraKey)
	})

	t.Run("provided boolean replaces current override", func(t *testing.T) {
		input := &UpdateProviderInput{Extra: map[string]any{GrokMediaEligibleExtraKey: true}}
		normalized, err := NormalizeGrokMediaEligibilityUpdateExtra(provider, input, map[string]any{GrokMediaEligibleExtraKey: true})

		require.NoError(t, err)
		require.Equal(t, true, normalized[GrokMediaEligibleExtraKey])
	})

	t.Run("malformed override is rejected on update", func(t *testing.T) {
		input := &UpdateProviderInput{Extra: map[string]any{GrokMediaEligibleExtraKey: "false"}}
		_, err := NormalizeGrokMediaEligibilityUpdateExtra(provider, input, nil)

		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, httpx.ErrorCode(err))
	})

	t.Run("non grok update is unchanged", func(t *testing.T) {
		input := &UpdateProviderInput{Extra: map[string]any{GrokMediaEligibleExtraKey: "provider-owned"}}
		normalized := map[string]any{GrokMediaEligibleExtraKey: "provider-owned"}
		got, err := NormalizeGrokMediaEligibilityUpdateExtra(&Record{Platform: capability.PlatformOpenAI}, input, normalized)

		require.NoError(t, err)
		require.Equal(t, normalized, got)
	})
}
