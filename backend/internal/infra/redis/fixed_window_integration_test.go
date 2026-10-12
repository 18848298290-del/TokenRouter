//go:build integration

package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Real Redis integration cases exercise counter ordering under simultaneous
// requests, a window whose TTL keeps decreasing, and repair of an existing
// counter without expiry. The bounded result channel holds every worker
// result before the test checks counts, admission totals, and observed TTLs.

// TestFixedWindowConcurrentCounts sends thirty-two concurrent requests
// against one Redis counter with a sixteen-request limit. It requires unique
// counts including one and thirty-two, sixteen admissions, and a positive TTL
// within the configured window.
func TestFixedWindowConcurrentCounts(t *testing.T) {
	client := startRedis(t, t.Context())
	limiter := NewFixedWindowLimiter(client, "rate_limit:")
	// result carries one worker's admission observation into the serial assertion pass.
	type result struct {
		allowed bool  // worker admission decision
		count   int64 // counter including worker request
		err     error // worker script error
	}
	// Buffer every worker result before waiting, then close after all senders
	// finish so result iteration neither blocks producers nor races channel closure.
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

// TestRateLimiterSetsTTLAndDoesNotRefresh samples a real counter's TTL around
// a later admitted request. It requires a positive initial lifetime and a
// smaller later lifetime, protecting the fixed window from renewal.
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

	// Let the real expiry decrease before the second request, making an
	// accidental lifetime renewal observable by comparing the two sampled TTLs.
	time.Sleep(50 * time.Millisecond)

	allowed, _, _, err = limiter.Allow(ctx, "ttl-test:127.0.0.1", 10, 2*time.Second)
	require.NoError(t, err)
	require.True(t, allowed)

	ttlAfter, err := rdb.PTTL(ctx, redisKey).Result()
	require.NoError(t, err)
	require.Less(t, ttlAfter, ttlBefore)
}

// TestRateLimiterFixesMissingTTL seeds an existing counter with no expiry
// before another request. It requires admission and a newly positive TTL,
// protecting repair of retained counters that otherwise never expire.
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
