package provider_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestOllamaCloudUsageManagedExtraCannotBeImported supplies forged remote usage
// fields during creation and CRS reconciliation. It checks creation rejects
// those fields, existing local session state survives an unchanged API Key,
// and rotating the identity removes the managed state.
func TestOllamaCloudUsageManagedExtraCannotBeImported(t *testing.T) {
	remoteExtra := map[string]any{
		provider.OllamaCloudUsageSessionExtraKey:     "remote-ciphertext",
		provider.OllamaCloudUsageAutoRefreshExtraKey: true,
		provider.OllamaCloudUsageSnapshotExtraKey:    map[string]any{"status": "forged"},
	}
	created, err := provider.BuildProviderForCreate(&provider.CreateProviderInput{
		Name: "ollama", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": "key"},
		Concurrency: 1,
	}, provider.CRSMergeMap(nil, remoteExtra), provider.CreationOptions{Now: time.Now, LoadLocation: time.LoadLocation, NewSeed: uuid.NewString})
	require.NoError(t, err)
	require.NotContains(t, created.Extra, provider.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, created.Extra, provider.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, created.Extra, provider.OllamaCloudUsageSnapshotExtraKey)

	existing := ollamaUsageProvider(6)
	existing.Extra = map[string]any{
		provider.OllamaCloudUsageSessionExtraKey:     "local-ciphertext",
		provider.OllamaCloudUsageAutoRefreshExtraKey: false,
		provider.OllamaCloudUsageSnapshotExtraKey:    map[string]any{"status": provider.OllamaCloudUsageStatusOK},
	}
	targetExtra := provider.CRSMergeMap(existing.Extra, remoteExtra)
	provider.ReconcileCRSOllamaCloudUsageExtra(existing, existing.Platform, existing.Type, provider.CRSMergeMap(existing.Credentials, nil), targetExtra)
	require.Equal(t, "local-ciphertext", targetExtra[provider.OllamaCloudUsageSessionExtraKey])
	require.Equal(t, false, targetExtra[provider.OllamaCloudUsageAutoRefreshExtraKey])
	require.Equal(t, map[string]any{"status": provider.OllamaCloudUsageStatusOK}, targetExtra[provider.OllamaCloudUsageSnapshotExtraKey])

	changedCredentials := provider.CRSMergeMap(existing.Credentials, map[string]any{"api_key": "rotated"})
	targetExtra = provider.CRSMergeMap(existing.Extra, remoteExtra)
	provider.ReconcileCRSOllamaCloudUsageExtra(existing, existing.Platform, existing.Type, changedCredentials, targetExtra)
	require.NotContains(t, targetExtra, provider.OllamaCloudUsageSessionExtraKey)
	require.NotContains(t, targetExtra, provider.OllamaCloudUsageAutoRefreshExtraKey)
	require.NotContains(t, targetExtra, provider.OllamaCloudUsageSnapshotExtraKey)
}
