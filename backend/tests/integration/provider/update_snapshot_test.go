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

// A configuration edit may begin with a record whose runtime timestamps are
// already stale. The PostgreSQL fixture writes newer activity and rate-limit
// timestamps before saving a rename, then reads the record to check that the
// runtime values remain stored.

// TestProviderConfigurationPreservesConcurrentRuntime reads a provider before
// sequentially writing newer activity and rate-limit timestamps. Saving a
// rename through the stale record must retain those stored runtime timestamps
// within the asserted microsecond tolerance.
func TestProviderConfigurationPreservesConcurrentRuntime(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	provider, err := client.Provider.Create().SetName(fmt.Sprintf("test-runtime-%d", time.Now().UnixNano())).SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeOAuth).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(provider.ID).Exec(context.Background())) })
	repo := newProviderStoreContract(client, integrationDB, nil)
	stale, err := repo.GetByID(ctx, provider.ID)
	require.NoError(t, err)
	// Keep runtime fixtures at PostgreSQL precision so the later microsecond
	// assertions detect a configuration write replacing either stored timestamp.
	usedAt := time.Now().UTC().Truncate(time.Microsecond)
	resetAt := usedAt.Add(time.Hour)
	// Write runtime timestamps through a separate committed statement after the
	// stale read, then rename to expose whether configuration writes replace them.
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
