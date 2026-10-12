//go:build integration

package app

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// TestNativeProviderHealthAssembly checks PostgreSQL reads and writes through assembled health components.
func TestNativeProviderHealthAssembly(t *testing.T) {
	f := newDatabaseFixture(t)
	store := providerpostgres.NewProviderStore(f.client, f.db, providerpostgres.ProviderStoreOptions{})
	cfg := &config.Config{}
	runtime := NewProviderHealthRuntimeForTest(store, nil, cfg, nil, nil, nil, nil, nil)
	observer := runtime.Observer
	require.Same(t, runtime.Health, observer.Core)
	require.Same(t, runtime.Health, observer.Limits.Health)
	require.Same(t, runtime.Health, observer.Models.Health)
	require.NotNil(t, runtime.Recovery)

	t.Run("anthropic window", func(t *testing.T) {
		row, err := f.client.Provider.Create().SetName("test-health-window").SetPlatform(provider.PlatformAnthropic).SetType(provider.ProviderTypeOAuth).Save(t.Context())
		require.NoError(t, err)
		value, err := store.GetByID(t.Context(), row.ID)
		require.NoError(t, err)
		reset := time.Now().Add(3 * time.Hour).Truncate(time.Second)
		headers := make(http.Header)
		headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
		headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset.Unix(), 10))
		observer.ApplyUpstreamError(t.Context(), value, provideradapter.HealthObservation{Status: 429, Headers: headers, Body: []byte("rate limited")})
		stored, err := store.GetByID(t.Context(), row.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.RateLimitResetAt)
		require.Equal(t, reset.Unix(), stored.RateLimitResetAt.Unix())
		require.NotNil(t, stored.SessionWindowEnd)
		require.Equal(t, reset.Unix(), stored.SessionWindowEnd.Unix())
		require.Equal(t, "rejected", stored.SessionWindowStatus)
	})

	t.Run("image scope", func(t *testing.T) {
		row, err := f.client.Provider.Create().SetName("test-health-image").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeAPIKey).Save(t.Context())
		require.NoError(t, err)
		value, err := store.GetByID(t.Context(), row.ID)
		require.NoError(t, err)
		require.True(t, provideradapter.ObserveOpenAIImageRateLimit(t.Context(), observer.Core, value, 429, http.Header{}, []byte(`{"error":{"message":"Rate limit reached for gpt-image. Please try again in 2s."}}`)))
		stored, err := store.GetByID(t.Context(), row.ID)
		require.NoError(t, err)
		require.NotNil(t, stored.ModelRateLimitResetAt(provider.OpenAIImageGenerationRateLimitKey))
		require.Nil(t, stored.RateLimitResetAt)
	})

	t.Run("shadow credential owner", func(t *testing.T) {
		parent, err := f.client.Provider.Create().SetName("test-health-parent").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeOAuth).SetCredentials(map[string]any{"refresh_token": "fixture-refresh"}).Save(t.Context())
		require.NoError(t, err)
		shadow, err := f.client.Provider.Create().SetName("test-health-shadow").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeOAuth).SetParentProviderID(parent.ID).SetQuotaDimension(provider.QuotaDimensionSpark).Save(t.Context())
		require.NoError(t, err)
		value, err := store.GetByID(t.Context(), shadow.ID)
		require.NoError(t, err)
		result := observer.ApplyUpstreamError(t.Context(), value, provideradapter.HealthObservation{Status: 401, Body: []byte("unauthorized")})
		require.True(t, result.StopScheduling)
		owner, err := store.GetByID(t.Context(), parent.ID)
		require.NoError(t, err)
		stored, err := store.GetByID(t.Context(), shadow.ID)
		require.NoError(t, err)
		require.NotNil(t, owner.TempUnschedulableUntil)
		require.Equal(t, provider.StatusActive, stored.Status)
		require.Nil(t, stored.TempUnschedulableUntil)
		require.Equal(t, "fixture-refresh", owner.GetCredential("refresh_token"))
	})
}
