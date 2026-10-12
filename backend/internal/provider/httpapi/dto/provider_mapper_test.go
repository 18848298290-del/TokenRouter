package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestProviderFromServiceShallow_RedactsSensitiveCredentials(t *testing.T) {
	src := &provider.Record{
		Now: time.Now, LoadLocation: time.LoadLocation,
		ID:       42,
		Name:     "demo",
		Platform: "anthropic",
		Type:     "oauth",
		Credentials: map[string]any{
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"id_token":      "id-secret",
			"api_key":       "sk-secret",
			"base_url":      "https://api.example.com",
			"model_mapping": map[string]any{"foo": "bar"},
		},
	}

	got := ProviderFromRecordShallow(src)
	require.NotNil(t, got)

	require.NotContains(t, got.Credentials, "access_token")
	require.NotContains(t, got.Credentials, "refresh_token")
	require.NotContains(t, got.Credentials, "id_token")
	require.NotContains(t, got.Credentials, "api_key")

	require.Equal(t, "https://api.example.com", got.Credentials["base_url"])
	require.Equal(t, map[string]any{"foo": "bar"}, got.Credentials["model_mapping"])

	require.True(t, got.CredentialsStatus["has_access_token"])
	require.True(t, got.CredentialsStatus["has_refresh_token"])
	require.True(t, got.CredentialsStatus["has_id_token"])
	require.True(t, got.CredentialsStatus["has_api_key"])

	// Check the serialized response as well as the DTO so secrets cannot leak through JSON.
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "rt-secret")
	require.NotContains(t, string(raw), "at-secret")
	require.NotContains(t, string(raw), "sk-secret")
	require.NotContains(t, string(raw), "id-secret")

	require.Contains(t, string(raw), "credentials_status")
	require.Contains(t, string(raw), "has_refresh_token")

	// Redaction leaves the source provider intact.
	require.Equal(t, "rt-secret", src.Credentials["refresh_token"])
}

func TestProviderFromServiceShallow_RedactsOllamaCloudManagedExtra(t *testing.T) {
	snapshot := map[string]any{
		"status":          provider.OllamaCloudUsageStatusOK,
		"last_attempt_at": "2026-07-22T12:00:00Z",
		"next_refresh_at": "2026-07-22T13:00:00Z",
		"data":            map[string]any{"plan": "Pro"},
	}
	src := &provider.Record{
		Now: time.Now, LoadLocation: time.LoadLocation,
		ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://ollama.com", "api_key": "secret-key",
			provider.NewAPIUserAccessTokenCredentialKey: "wallet-token-secret",
		},
		Extra: map[string]any{
			provider.OllamaCloudUsageSessionExtraKey:     "ciphertext-secret",
			provider.OllamaCloudUsageAutoRefreshExtraKey: true,
			provider.OllamaCloudUsageSnapshotExtraKey:    snapshot,
			"ordinary": "kept",
		},
	}

	got := ProviderFromRecordShallow(src)
	require.NotContains(t, got.Extra, provider.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, got.Extra, provider.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, got.Extra, provider.OllamaCloudUsageSnapshotExtraKey)
	require.Equal(t, "kept", got.Extra["ordinary"])
	require.NotNil(t, got.OllamaCloudUsage)
	require.True(t, got.OllamaCloudUsage.Configured)
	require.True(t, got.OllamaCloudUsage.AutoRefreshEnabled)
	require.Equal(t, "Pro", got.OllamaCloudUsage.Snapshot.Data.Plan)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "ciphertext-secret")
	require.NotContains(t, string(raw), "secret-key")
	require.NotContains(t, string(raw), "wallet-token-secret")
	require.Contains(t, src.Extra, provider.OllamaCloudUsageSessionExtraKey)
}

func TestProviderFromServiceShallow_RedactsLegacyUpstreamUsageSecrets(t *testing.T) {
	src := &provider.Record{
		Now: time.Now, LoadLocation: time.LoadLocation,
		ID: 10, Type: capability.ProviderTypeAPIKey,
		Extra: map[string]any{
			provider.UpstreamUsageQueryExtraKey: map[string]any{
				"enabled": true, "adapter": "legacy-secret", "base_url": "https://user:legacy-secret@gateway.example?token=legacy-secret",
				"api_key": "legacy-secret", "headers": map[string]any{"Authorization": "Bearer legacy-secret"},
			},
		},
	}
	got := ProviderFromRecordShallow(src)
	require.Equal(t, map[string]any{
		"enabled": true,
	}, got.Extra[provider.UpstreamUsageQueryExtraKey])
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "legacy-secret")
	// Mapping leaves historical database values intact.
	legacyConfig, ok := src.Extra[provider.UpstreamUsageQueryExtraKey].(map[string]any)
	require.True(t, ok)
	require.Contains(t, legacyConfig, "api_key")
}

func TestProviderFromServiceShallow_PreservesZivvAdapterSelection(t *testing.T) {
	src := &provider.Record{
		Now: time.Now, LoadLocation: time.LoadLocation,
		ID: 11, Type: capability.ProviderTypeAPIKey,
		Extra: map[string]any{provider.UpstreamUsageQueryExtraKey: map[string]any{
			"enabled": true, "adapter": provider.UpstreamUsageAdapterZivv,
		}},
	}
	got := ProviderFromRecordShallow(src)
	require.Equal(t, map[string]any{
		"enabled": true, "adapter": provider.UpstreamUsageAdapterZivv,
	}, got.Extra[provider.UpstreamUsageQueryExtraKey])
}

func TestProviderFromServiceShallow_NilCredentialsOmitsStatus(t *testing.T) {
	src := &provider.Record{Now: time.Now, LoadLocation: time.LoadLocation, ID: 1, Name: "n", Platform: "anthropic", Type: "oauth"}
	got := ProviderFromRecordShallow(src)
	require.NotNil(t, got)
	require.Nil(t, got.Credentials)
	require.Nil(t, got.CredentialsStatus)
}

func TestProviderFromServiceShallow_OpenAIOAuthTLSFingerprint(t *testing.T) {
	src := &provider.Record{
		Now: time.Now, LoadLocation: time.LoadLocation,
		ID:       3,
		Name:     "openai-oauth",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": -1,
		},
	}

	got := ProviderFromRecordShallow(src)
	require.NotNil(t, got)
	require.NotNil(t, got.EnableTLSFingerprint)
	require.True(t, *got.EnableTLSFingerprint)
	require.NotNil(t, got.TLSFingerprintProfileID)
	require.Equal(t, int64(-1), *got.TLSFingerprintProfileID)
}

// TestProviderDTOHasIndependentNestedValues checks that editing nested DTO values leaves source maps intact.
func TestProviderDTOHasIndependentNestedValues(t *testing.T) {
	value := &provider.Record{Now: time.Now, LoadLocation: time.LoadLocation, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "model"}}, Extra: map[string]any{"policy": map[string]any{"enabled": true}}}
	view := ProviderFromRecordShallow(value)
	mapping, ok := view.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	policy, ok := view.Extra["policy"].(map[string]any)
	require.True(t, ok)
	mapping["alias"] = "changed"
	policy["enabled"] = false
	require.Equal(t, map[string]any{"model_mapping": map[string]any{"alias": "model"}}, value.Credentials)
	require.Equal(t, map[string]any{"policy": map[string]any{"enabled": true}}, value.Extra)
}
