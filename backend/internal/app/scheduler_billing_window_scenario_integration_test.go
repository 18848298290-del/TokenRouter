//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Session and billing-window caches use independent Redis keys for the same
// provider identity. The scenario writes both, releases the session, and checks
// the cost remains available with its bounded TTL before another session is
// admitted through the shared client.

// TestSessionAndWindowCachesKeepIndependentState writes a session and window
// cost through application cache constructors. It checks Redis cost keys and
// TTL, session counts, retained cost after release, missing batch entries, and
// a subsequent successful claim.
func TestSessionAndWindowCachesKeepIndependentState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	// Register any created container before startup errors can stop this test.
	// Later client cleanups run first because testing reverses registration order.
	if container != nil {
		t.Cleanup(func() {
			// Setup or test cancellation must not cancel Docker removal. Start the
			// bounded cleanup context when this callback runs.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate Redis container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	endpoint, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(endpoint)
	require.NoError(t, err)
	rdb := redis.NewClient(options)
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	sessions := provideSessionCache(rdb, &config.Config{})
	windows := provideWindowCostCache(rdb)
	const providerID int64 = 3301
	allowed, err := sessions.RegisterSession(ctx, providerID, "first", 1, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, windows.SetWindowCost(ctx, providerID, 12.5))
	stored, err := rdb.Get(ctx, "window_cost:provider:3301").Float64()
	require.NoError(t, err)
	require.Equal(t, 12.5, stored)
	ttl, err := rdb.PTTL(ctx, "window_cost:provider:3301").Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, 30*time.Second)
	active, err := sessions.GetActiveSessionCount(ctx, providerID)
	require.NoError(t, err)
	require.Equal(t, 1, active)

	require.NoError(t, sessions.UnregisterSession(ctx, providerID, "first"))
	cost, hit, err := windows.GetWindowCost(ctx, providerID)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, 12.5, cost)
	batch, err := windows.GetWindowCostBatch(ctx, []int64{providerID, providerID + 1})
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{providerID: 12.5}, batch)
	allowed, err = sessions.RegisterSession(ctx, providerID, "second", 1, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
}
