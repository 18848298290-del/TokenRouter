//go:build integration

package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFixedWindowConcurrentCounts checks unique counts, admission totals, and window TTL.
func TestFixedWindowConcurrentCounts(t *testing.T) {
	client := startRedis(t, t.Context())
	limiter := NewFixedWindowLimiter(client, "rate_limit:")
	type result struct {
		allowed bool
		count   int64
		err     error
	}
	results := make(chan result, 32)
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			allowed, count, _, err := limiter.Allow(t.Context(), "concurrent", 16, 2*time.Second)
			results <- result{allowed, count, err}
		})
	}
	group.Wait()
	close(results)
	counts := map[int64]bool{}
	allowed := 0
	for value := range results {
		require.NoError(t, value.err)
		counts[value.count] = true
		if value.allowed {
			allowed++
		}
	}
	require.Len(t, counts, 32)
	require.True(t, counts[1])
	require.True(t, counts[32])
	require.Equal(t, 16, allowed)
	ttl, err := client.PTTL(t.Context(), "rate_limit:concurrent").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
	require.LessOrEqual(t, ttl, 2*time.Second)
}

func TestRateLimiterSetsTTLAndDoesNotRefresh(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t, ctx)
	limiter := NewFixedWindowLimiter(rdb, "rate_limit:")

	allowed, _, _, err := limiter.Allow(ctx, "ttl-test:127.0.0.1", 10, 2*time.Second)
	require.NoError(t, err)
	require.True(t, allowed)

	redisKey := limiter.prefix + "ttl-test:127.0.0.1"
	ttlBefore, err := rdb.PTTL(ctx, redisKey).Result()
	require.NoError(t, err)
	require.Greater(t, ttlBefore, time.Duration(0))
	require.LessOrEqual(t, ttlBefore, 2*time.Second)

	time.Sleep(50 * time.Millisecond)

	allowed, _, _, err = limiter.Allow(ctx, "ttl-test:127.0.0.1", 10, 2*time.Second)
	require.NoError(t, err)
	require.True(t, allowed)

	ttlAfter, err := rdb.PTTL(ctx, redisKey).Result()
	require.NoError(t, err)
	require.Less(t, ttlAfter, ttlBefore)
}

func TestRateLimiterFixesMissingTTL(t *testing.T) {
	ctx := context.Background()
	rdb := startRedis(t, ctx)
	limiter := NewFixedWindowLimiter(rdb, "rate_limit:")

	redisKey := limiter.prefix + "ttl-missing:127.0.0.1"
	require.NoError(t, rdb.Set(ctx, redisKey, 5, 0).Err())

	ttlBefore, err := rdb.PTTL(ctx, redisKey).Result()
	require.NoError(t, err)
	require.Less(t, ttlBefore, time.Duration(0))

	allowed, _, _, err := limiter.Allow(ctx, "ttl-missing:127.0.0.1", 10, 2*time.Second)
	require.NoError(t, err)
	require.True(t, allowed)

	ttlAfter, err := rdb.PTTL(ctx, redisKey).Result()
	require.NoError(t, err)
	require.Greater(t, ttlAfter, time.Duration(0))
}
