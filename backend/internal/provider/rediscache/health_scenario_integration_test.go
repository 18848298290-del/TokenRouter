//go:build integration

package rediscache

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Independent provider cache instances share failure counters and temporary
// suspension in Redis. These scenarios cover health_counter.go and
// temp_unsched_cache.go, checking stable key names, first-count expiry,
// concurrent count allocation, extend-only deadlines, malformed state errors,
// and rolling-threshold cleanup against a real server.

// TestProviderHealthRedisCompatibility uses independent adapters against one
// real Redis client to check shared namespaces and Lua results. It protects
// first-count expiry, unique concurrent counts, extend-only suspension,
// malformed-state errors, and cleanup when the rolling failure threshold
// trips.
func TestProviderHealthRedisCompatibility(t *testing.T) {
	client := rediscontainer.New(t)
	ctx := context.Background()
	t.Run("internal500_count_and_fixed_ttl", func(t *testing.T) {
		first := NewInternal500CounterCache(client)
		second := NewInternal500CounterCache(client)
		key := "internal500_count:provider:905"
		count, err := first.IncrementInternal500Count(ctx, 905)
		require.NoError(t, err)
		require.EqualValues(t, 1, count)
		ttl, err := client.TTL(ctx, key).Result()
		require.NoError(t, err)
		// Leave a setup margin when checking the first-count expiry.
		require.Greater(t, ttl, 23*time.Hour)
		require.LessOrEqual(t, ttl, 24*time.Hour)
		// Override the first INTERNAL 500 lifetime so a later shared increment
		// exposes any accidental restart of the twenty-four-hour window through
		// its TTL.
		require.NoError(t, client.PExpire(ctx, key, 20*time.Second).Err())
		count, err = second.IncrementInternal500Count(ctx, 905)
		require.NoError(t, err)
		require.EqualValues(t, 2, count)
		ttl, err = client.PTTL(ctx, key).Result()
		require.NoError(t, err)
		require.Greater(t, ttl, 15*time.Second)
		require.LessOrEqual(t, ttl, 20*time.Second)
		require.NoError(t, first.ResetInternal500Count(ctx, 905))
		require.Zero(t, client.Exists(ctx, key).Val())
	})
	t.Run("403_count_and_fixed_ttl", func(t *testing.T) {
		old := NewOpenAI403CounterCache(client)
		current := NewOpenAI403CounterCache(client)
		key := "openai_403_count:provider:901"
		n, err := old.IncrementOpenAI403Count(ctx, 901, 0)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		ttl, err := client.TTL(ctx, key).Result()
		require.NoError(t, err)
		require.LessOrEqual(t, ttl, time.Minute)
		// Allow setup elapsed time inside the one-minute lower bound.
		require.Greater(t, ttl, 50*time.Second)
		// Sample a shortened 403 window before supplying a five-minute duration;
		// the second adapter keeps the first expiry rather than installing
		// that new window.
		require.NoError(t, client.PExpire(ctx, key, 20*time.Second).Err())
		n, err = current.IncrementOpenAI403Count(ctx, 901, 5)
		require.NoError(t, err)
		require.EqualValues(t, 2, n)
		ttl, err = client.PTTL(ctx, key).Result()
		require.NoError(t, err)
		require.LessOrEqual(t, ttl, 20*time.Second)
		require.Greater(t, ttl, 15*time.Second)
		require.NoError(t, current.ResetOpenAI403Count(ctx, 901))
		require.Zero(t, client.Exists(ctx, key).Val())
	})
	t.Run("timeout_concurrent_shared_counter", func(t *testing.T) {
		stores := []provider.TimeoutCounterCache{NewTimeoutCounterCache(client), NewTimeoutCounterCache(client)}
		// result carries one concurrent increment's value and failure to the
		// assertion loop.
		type result struct {
			count int64 // allocated shared failure count
			err   error // Redis script invocation failure
		}
		// Buffer one result per worker so completion does not wait for the assertion
		// reader, which starts only after the full shared-counter round has finished.
		results := make(chan result, 40)
		var wg sync.WaitGroup
		for i := range 40 {
			wg.Go(func() { n, err := stores[i%2].IncrementTimeoutCount(ctx, 902, 1); results <- result{n, err} })
		}
		// Wait for all sends before closing the channel; the following range then
		// observes the complete counter allocation and terminates without
		// an extra signal.
		wg.Wait()
		close(results)
		values := make([]int, 0, 40)
		for result := range results {
			require.NoError(t, result.err)
			values = append(values, int(result.count))
		}
		sort.Ints(values)
		for i, v := range values {
			require.Equal(t, i+1, v)
		}
		// Shorten timeout expiry before an eight-minute increment so the following
		// TTL assertion detects whether that increment restarts the existing window.
		require.NoError(t, client.PExpire(ctx, "timeout_count:provider:902", 20*time.Second).Err())
		n, err := stores[0].IncrementTimeoutCount(ctx, 902, 8)
		require.NoError(t, err)
		require.EqualValues(t, 41, n)
		ttl, err := stores[1].GetTimeoutCountTTL(ctx, 902)
		require.NoError(t, err)
		require.LessOrEqual(t, ttl, 20*time.Second)
		require.NoError(t, stores[1].ResetTimeoutCount(ctx, 902))
		n, err = stores[0].GetTimeoutCount(ctx, 902)
		require.NoError(t, err)
		require.Zero(t, n)
	})
	t.Run("temporary_state_extends_only", func(t *testing.T) {
		old := NewTempUnschedCache(client)
		current := NewTempUnschedCache(client)
		now := time.Now().Unix()
		longer := &provider.TempUnschedState{UntilUnix: now + 300, StatusCode: 429, ErrorMessage: "long"}
		require.NoError(t, old.SetTempUnsched(ctx, 903, longer))
		require.NoError(t, current.SetTempUnsched(ctx, 903, &provider.TempUnschedState{UntilUnix: now + 100, StatusCode: 403}))
		observed, err := current.GetTempUnsched(ctx, 903)
		require.NoError(t, err)
		require.Equal(t, longer, observed)
		require.NoError(t, current.DeleteTempUnsched(ctx, 903))
		observed, err = old.GetTempUnsched(ctx, 903)
		require.NoError(t, err)
		require.Nil(t, observed)
		require.NoError(t, client.Set(ctx, "temp_unsched:provider:903", "broken", time.Minute).Err())
		_, err = current.GetTempUnsched(ctx, 903)
		require.Error(t, err)
	})
	t.Run("rolling_threshold_keeps_legacy_keys", func(t *testing.T) {
		old, ok := NewTempUnschedCache(client).(provider.OpenAIAPIKeyHealthCache)
		require.True(t, ok)
		current, ok := NewTempUnschedCache(client).(provider.OpenAIAPIKeyHealthCache)
		require.True(t, ok)
		for i := 1; i <= 3; i++ {
			store := old
			if i == 2 {
				store = current
			}
			n, tripped, err := store.RecordOpenAIAPIKeyHealthFailure(ctx, 904, 1, 3)
			require.NoError(t, err)
			require.EqualValues(t, i, n)
			require.Equal(t, i == 3, tripped)
		}
		require.Zero(t, client.Exists(ctx, fmt.Sprintf("openai_apikey_health:{%d}:failures", 904), fmt.Sprintf("openai_apikey_health:{%d}:failures:sequence", 904)).Val())
	})
}
