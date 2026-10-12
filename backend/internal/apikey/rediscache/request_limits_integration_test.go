//go:build integration

package rediscache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// TestRequestLimitsAtomicAdmission races distinct request identities against
// each configured Redis allowance combination. It checks shared admission
// count, replay idempotence, rejection isolation, retained RPM history,
// independent Key IDs and pruning of elapsed records.
func TestRequestLimitsAtomicAdmission(t *testing.T) {
	client := rediscontainer.New(t)
	cache := &ApiKeyCache{rdb: client}
	ctx := context.Background()
	for _, tc := range []struct {
		name string // allowance combination scenario label
		// concurrency sets active capacity and rpm sets rolling admission capacity.
		// Their combinations share one expected limit while exercising independent
		// and combined scripts against the same request population.
		concurrency, rpm int // active request allowance; rolling admission allowance
	}{
		{"concurrency", 3, 0}, {"rpm", 0, 3}, {"both", 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Clear prior-case members before this allowance combination so its
			// accepted count reflects the fresh concurrent population instead of retained request history.
			require.NoError(t, client.FlushDB(ctx).Err())
			var accepted atomic.Int64
			var wg sync.WaitGroup
			for i := range 24 {
				wg.Add(1)
				// Distinct identities compete inside Redis, so the joined accepted count
				// exposes any over-admission across the script's shared sorted-set capacity checks.
				go func(i int) {
					defer wg.Done()
					_, err := cache.ReserveRequest(ctx, 1, fmt.Sprint(i), tc.concurrency, tc.rpm)
					if err == nil {
						accepted.Add(1)
					}
				}(i)
			}
			wg.Wait()
			require.EqualValues(t, 3, accepted.Load())
		})
	}
	// Replay the same identity before its stored windows expire, checking
	// that command repetition keeps one member and its first RPM timestamp.
	require.NoError(t, client.FlushDB(ctx).Err())
	for _, limits := range [][2]int{{1, 0}, {0, 1}, {1, 1}} {
		require.NoError(t, client.Del(ctx, requestLimitKeys(9)...).Err())
		_, err := cache.ReserveRequest(ctx, 9, "replayed", limits[0], limits[1])
		require.NoError(t, err)
		score := client.ZScore(ctx, requestLimitKeys(9)[1], "replayed").Val()
		_, err = cache.ReserveRequest(ctx, 9, "replayed", limits[0], limits[1])
		require.NoError(t, err)
		require.Equal(t, score, client.ZScore(ctx, requestLimitKeys(9)[1], "replayed").Val())
		if limits[0] > 0 {
			require.EqualValues(t, 1, client.ZCard(ctx, requestLimitKeys(9)[0]).Val())
		}
		if limits[1] > 0 {
			require.EqualValues(t, 1, client.ZCard(ctx, requestLimitKeys(9)[1]).Val())
		}
	}
	// Reject the second active request before releasing the first lease;
	// later successful admission shows that rejection left the other RPM position available.
	require.NoError(t, client.FlushDB(ctx).Err())
	_, err := cache.ReserveRequest(ctx, 2, "first", 1, 2)
	require.NoError(t, err)
	_, err = cache.ReserveRequest(ctx, 2, "blocked", 1, 2)
	require.ErrorIs(t, err, apikey.ErrKeyConcurrencyExceeded)
	require.NoError(t, cache.ReleaseRequest(ctx, 2, "first"))
	_, err = cache.ReserveRequest(ctx, 2, "second", 1, 2)
	require.NoError(t, err)
	require.NoError(t, cache.ReleaseRequest(ctx, 2, "second"))
	retry, err := cache.ReserveRequest(ctx, 2, "third", 1, 2)
	require.ErrorIs(t, err, apikey.ErrKeyRPMExceeded)
	require.Positive(t, retry)
	require.LessOrEqual(t, retry, time.Minute)
	require.Zero(t, client.ZCard(ctx, requestLimitKeys(2)[0]).Val())
	// Supply another Key ID after the first Key exhausts RPM to check its
	// independent allowance, then verify that released lease renewal cannot resurrect it.
	_, err = cache.ReserveRequest(ctx, 3, "other", 1, 2)
	require.NoError(t, err)
	require.NoError(t, cache.RefreshRequest(ctx, 3, "other"))
	require.NoError(t, cache.ReleaseRequest(ctx, 3, "other"))
	require.ErrorIs(t, cache.RefreshRequest(ctx, 3, "other"), apikey.ErrKeyLimiterUnavailable)
	// Insert records older than both windows so subsequent script admission
	// must reclaim their capacity before accepting the fresh request identity.
	now := time.Now().Add(-3 * time.Minute).UnixMilli()
	require.NoError(t, client.ZAdd(ctx, requestLimitKeys(4)[0], redis.Z{Score: float64(now), Member: "stale"}).Err())
	require.NoError(t, client.ZAdd(ctx, requestLimitKeys(4)[1], redis.Z{Score: float64(now), Member: "stale"}).Err())
	_, err = cache.ReserveRequest(ctx, 4, "fresh", 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, client.ZCard(ctx, requestLimitKeys(4)[0]).Val())
}
