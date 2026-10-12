//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestProviderConfigurationPreservesConcurrentRuntime checks that a rename from a stale record
// preserves newer last-used and rate-limit timestamps.
func TestProviderConfigurationPreservesConcurrentRuntime(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	provider, err := client.Provider.Create().SetName(fmt.Sprintf("test-runtime-%d", time.Now().UnixNano())).SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeOAuth).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(provider.ID).Exec(context.Background())) })
	repo := newProviderStoreContract(client, integrationDB, nil)
	stale, err := repo.GetByID(ctx, provider.ID)
	require.NoError(t, err)
	usedAt := time.Now().UTC().Truncate(time.Microsecond)
	resetAt := usedAt.Add(time.Hour)
	_, err = integrationDB.ExecContext(ctx, "UPDATE providers SET last_used_at=$1,rate_limited_at=$1,rate_limit_reset_at=$2 WHERE id=$3", usedAt, resetAt, provider.ID)
	require.NoError(t, err)
	stale.Name = "name-edited"
	require.NoError(t, repo.Update(ctx, stale))
	current, err := repo.GetByID(ctx, provider.ID)
	require.NoError(t, err)
	require.Equal(t, "name-edited", current.Name)
	require.NotNil(t, current.LastUsedAt)
	require.WithinDuration(t, usedAt, *current.LastUsedAt, time.Microsecond)
	require.NotNil(t, current.RateLimitResetAt)
	require.WithinDuration(t, resetAt, *current.RateLimitResetAt, time.Microsecond)
}
