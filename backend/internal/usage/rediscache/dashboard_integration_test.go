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

// TestDashboardCacheLegacyKeyAndTTL checks direct Redis interoperability through the persisted cache key and TTL.
func TestDashboardCacheLegacyKeyAndTTL(t *testing.T) {
	ctx := context.Background()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	url, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(url)
	require.NoError(t, err)
	raw := redis.NewClient(options)
	t.Cleanup(func() { _ = raw.Close() })
	cache := NewDashboardCache(raw, "prod")
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
