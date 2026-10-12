package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// PAT updates can arrive after OAuth metadata was merged into the credential
// map. This case verifies removal of refresh-only state and publication of PAT
// markers while retaining the access token, subscription identity and custom
// configuration entries.

// TestNormalizeOpenAIPersonalAccessTokenCredentialsRemovesOAuthFields
// classifies PAT mode from a stored provider and normalizes a mixed credential
// map. OAuth refresh fields disappear while authentication markers, account
// metadata and the asserted custom entries remain.
func TestNormalizeOpenAIPersonalAccessTokenCredentialsRemovesOAuthFields(t *testing.T) {
	provider := &Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"auth_mode": "personal_access_token",
		},
	}
	credentials := map[string]any{
		"access_token":                "at-test-token",
		"refresh_token":               "stale-refresh-token",
		"id_token":                    "stale-id-token",
		"expires_at":                  "2026-01-01T00:00:00Z",
		"expires_in":                  3600,
		"client_id":                   "stale-client",
		"model_mapping":               map[string]any{"gpt-5": "gpt-5-codex"},
		"chatgpt_account_is_fedramp":  true,
		"subscription_expires_at":     "2026-12-31T00:00:00Z",
		"openai_usage_channel_fields": []any{"custom"},
	}

	got := NormalizeOpenAIPersonalAccessTokenCredentials(provider, nil, credentials)

	require.Equal(t, "at-test-token", got["access_token"])
	require.Equal(t, OpenAIAuthModePersonalAccessToken, got["auth_mode"])
	require.Equal(t, "personal_access_token", got["openai_auth_mode"])
	require.Equal(t, "Bearer", got["token_type"])
	require.NotContains(t, got, "refresh_token")
	require.NotContains(t, got, "id_token")
	require.NotContains(t, got, "expires_at")
	require.NotContains(t, got, "expires_in")
	require.NotContains(t, got, "client_id")
	require.Equal(t, map[string]any{"gpt-5": "gpt-5-codex"}, got["model_mapping"])
	require.Equal(t, true, got["chatgpt_account_is_fedramp"])
	require.Equal(t, "2026-12-31T00:00:00Z", got["subscription_expires_at"])
	require.Equal(t, []any{"custom"}, got["openai_usage_channel_fields"])
}
