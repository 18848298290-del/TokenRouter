package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Provider mapper responses are checked both as DTO values and serialized JSON
// so removed secret values cannot reappear during encoding. Fixtures cover
// credentials, Ollama-managed Extra, historical usage query redaction and
// supported nested map ownership in provider_mapper.go.

// TestProviderFromServiceShallow_RedactsSensitiveCredentials maps an Anthropic
// OAuth record with tokens and ordinary model configuration. DTO and JSON
// output omit the supplied secrets while presence flags and source credentials
// remain available.
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

// TestProviderFromServiceShallow_RedactsOllamaCloudManagedExtra maps eligible
// Ollama configuration with encrypted session and saved usage. Raw managed
// Extra and credential secrets disappear from JSON, while the separate usage
// view retains its display state.
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

// TestProviderFromServiceShallow_RedactsLegacyUpstreamUsageSecrets provides
// historical usage configuration containing secret-shaped adapter and URL
// fields. Only permitted enablement remains visible, and the original
// historical object still contains its input keys.
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
	// The fixture's retained source object still contains its historical input.
	legacyConfig, ok := src.Extra[provider.UpstreamUsageQueryExtraKey].(map[string]any)
	require.True(t, ok)
	require.Contains(t, legacyConfig, "api_key")
}

// TestProviderFromServiceShallow_PreservesZivvAdapterSelection provides a
// registered Zivv adapter in usage-query configuration. The mapped Extra keeps
// its enabled flag and adapter name together for administrator display.
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

// TestProviderFromServiceShallow_NilCredentialsOmitsStatus maps an OAuth record
// whose credential map is absent. The resulting DTO keeps both credentials and
// sensitive-presence status nil, preserving the missing-input representation.
func TestProviderFromServiceShallow_NilCredentialsOmitsStatus(t *testing.T) {
	src := &provider.Record{Now: time.Now, LoadLocation: time.LoadLocation, ID: 1, Name: "n", Platform: "anthropic", Type: "oauth"}
	got := ProviderFromRecordShallow(src)
	require.NotNil(t, got)
	require.Nil(t, got.Credentials)
	require.Nil(t, got.CredentialsStatus)
}

// TestProviderFromServiceShallow_OpenAIOAuthTLSFingerprint maps an OpenAI OAuth
// record with fingerprint enablement and profile -1. The optional DTO fields
// retain true and that exact nonzero selector for JSON presentation.
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

// TestProviderDTOHasIndependentNestedValues edits nested model mapping and
// policy maps in a returned DTO. Those supported map containers stay
// independent of the corresponding source credential and Extra maps.
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
