package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Messages authentication scheme selection depends on provider platform and
// authentication type as well as the Extra value. Fixtures cover default,
// explicit, and unrecognized settings and compare an unsupported platform with
// a CN API-key record.

// TestProvider_GetAnthropicAPIKeyAuthScheme supplies default, bearer, invalid,
// and unsupported-platform configurations. Each result must select the asserted
// scheme while keeping the unsupported provider on the default header choice.
func TestProvider_GetAnthropicAPIKeyAuthScheme(t *testing.T) {
	tests := []struct {
		name     string  // named authentication scheme scenario
		provider *Record // configured provider record fixture
		want     string  // expected authentication header scheme
	}{
		{
			name: "缺省使用 x-api-key",
			provider: &Record{
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeAPIKey,
			},
			want: AnthropicAPIKeyAuthSchemeXAPIKey,
		},
		{
			name: "显式使用 bearer",
			provider: &Record{
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"anthropic_apikey_auth_scheme": AnthropicAPIKeyAuthSchemeAuthorizationBearer,
				},
			},
			want: AnthropicAPIKeyAuthSchemeAuthorizationBearer,
		},
		{
			name: "非法值回退 x-api-key",
			provider: &Record{
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"anthropic_apikey_auth_scheme": "bearer",
				},
			},
			want: AnthropicAPIKeyAuthSchemeXAPIKey,
		},
		{
			name: "非 Anthropic API Key 回退 x-api-key",
			provider: &Record{
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeAPIKey,
				Extra: map[string]any{
					"anthropic_apikey_auth_scheme": AnthropicAPIKeyAuthSchemeAuthorizationBearer,
				},
			},
			want: AnthropicAPIKeyAuthSchemeXAPIKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.provider.GetAnthropicAPIKeyAuthScheme())
		})
	}
}

// TestGetAnthropicAPIKeyAuthScheme_CNProvider uses a Zhipu API-key record for
// the Messages protocol. Its missing Extra value must select x-api-key, and an
// explicit bearer setting must replace that selection.
func TestGetAnthropicAPIKeyAuthScheme_CNProvider(t *testing.T) {
	t.Parallel()

	zhipu := &Record{
		Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}
	require.Equal(t, AnthropicAPIKeyAuthSchemeXAPIKey, zhipu.GetAnthropicAPIKeyAuthScheme())

	zhipu.Extra = map[string]any{"anthropic_apikey_auth_scheme": "authorization_bearer"}
	require.Equal(t, AnthropicAPIKeyAuthSchemeAuthorizationBearer, zhipu.GetAnthropicAPIKeyAuthScheme())
}
