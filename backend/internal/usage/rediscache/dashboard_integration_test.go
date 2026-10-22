//go:build integration

package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Dashboard cache text and expiration must remain visible through direct Redis
// access at the versioned key. A real Redis fixture alternates raw-client
// writes with adapter reads and adapter writes with raw-client reads. It also
// checks deletion's cache-miss result and the unprefixed key.

// TestDashboardCacheLegacyKeyAndTTL exchanges serialized dashboard text between
// a raw Redis client and the cache adapter. The versioned prefixed key, bounded
// expiration, deletion miss and empty-prefix key remain observable through
// those operations.
func TestDashboardCacheLegacyKeyAndTTL(t *testing.T) {
	ctx := context.Background()
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
	url, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(url)
	require.NoError(t, err)
	raw := redis.NewClient(options)
	// Close the client during test cleanup after its final cache assertion;
	// registration order releases it before terminating the earlier-created
	// container.
	t.Cleanup(func() { _ = raw.Close() })
	cache := NewDashboardCache(raw, "prod")
	// Use the persisted versioned key directly so adapter reads cannot pass by
	// consulting a different private key from the raw-client fixture.
	const key = "prod:dashboard:stats:v2"
	const legacy = `{"total_requests":7,"total_actual_cost":0.125}`
	require.NoError(t, raw.Set(ctx, key, legacy, time.Minute).Err())
	value, err := cache.GetDashboardStats(ctx)
	require.NoError(t, err)
	require.Equal(t, legacy, value)
	const next = `{"total_requests":8}`
	require.NoError(t, cache.SetDashboardStats(ctx, next, 2*time.Minute))
	value, err = raw.Get(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, next, value)
	ttl, err := raw.PTTL(ctx, key).Result()
	require.NoError(t, err)
	// Allow elapsed milliseconds below the written two-minute duration while
	// checking that these immediate reads retain more than one minute of
	// expiration.
	require.Greater(t, ttl, time.Minute)
	require.LessOrEqual(t, ttl, 2*time.Minute)
	require.NoError(t, cache.DeleteDashboardStats(ctx))
	_, err = cache.GetDashboardStats(ctx)
	require.ErrorIs(t, err, usage.ErrDashboardStatsCacheMiss)
	unprefixed := NewDashboardCache(raw, "")
	require.NoError(t, unprefixed.SetDashboardStats(ctx, next, time.Minute))
	value, err = raw.Get(ctx, "dashboard:stats:v2").Result()
	require.NoError(t, err)
	require.Equal(t, next, value)
}
