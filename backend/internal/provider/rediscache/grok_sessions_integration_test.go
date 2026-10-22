//go:build integration

package rediscache

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Grok OAuth sessions need a stable Redis representation across independently
// constructed stores. This real Redis test covers grok_sessions.go's
// namespace, JSON and lifetime binding, then contends on the shared
// consumption marker and confirms remote deletion prevents the core from
// returning a stale local session.

// TestGrokSessionsRedisWireAndSingleConsumption constructs two Grok stores
// over one real Redis client and reads a manually encoded session. It checks
// key spelling, JSON and TTL, requires one successful concurrent claim, and
// confirms remote deletion prevents a cached copy from returning.
func TestGrokSessionsRedisWireAndSingleConsumption(t *testing.T) {
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
	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(uri)
	require.NoError(t, err)
	client := redis.NewClient(options)
	// The store cleanups registered below run before client closure, keeping the
	// borrowed Redis connection usable for every remaining session-store operation.
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	first := NewGrokSessionStore(client)
	second := NewGrokSessionStore(client)
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)
	original := &provider.GrokOAuthSession{State: "fixture", CodeVerifier: "verifier", CreatedAt: time.Now().UTC()}
	encoded, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, "oauth:session:xai:old-wire", encoded, provider.GrokSessionTTL).Err())
	value, ok := first.Get("old-wire")
	require.True(t, ok)
	require.Equal(t, original.State, value.State)
	require.Equal(t, original.CodeVerifier, value.CodeVerifier)
	require.WithinDuration(t, original.CreatedAt, value.CreatedAt, time.Nanosecond)
	first.Set("new-wire", original)
	raw, err := client.Get(ctx, "oauth:session:xai:new-wire").Bytes()
	require.NoError(t, err)
	var decoded provider.GrokOAuthSession
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, original.State, decoded.State)
	ttl, err := client.TTL(ctx, "oauth:session:xai:new-wire").Result()
	require.NoError(t, err)
	// Allow setup elapsed time while checking the shared lifetime remains close
	// to thirty minutes; Redis TTL observations arrive with whole-second resolution.
	require.Greater(t, ttl, provider.GrokSessionTTL-time.Minute)
	require.LessOrEqual(t, ttl, provider.GrokSessionTTL)
	var claimed atomic.Int64
	var workers sync.WaitGroup
	// Alternate stores within one contention round so the single successful claim
	// comes from the shared marker rather than an individual store's local mutex.
	for i := range 32 {
		workers.Go(func() {
			store := first
			if i%2 == 1 {
				store = second
			}
			if store.TryConsumeSession("new-wire") {
				claimed.Add(1)
			}
		})
	}
	// Observe the final count after every claim finishes, so the assertion includes
	// slower Redis callers instead of sampling a partially completed contention round.
	workers.Wait()
	require.EqualValues(t, 1, claimed.Load())
	marker, err := client.Get(ctx, "oauth:session:xai:used:new-wire").Result()
	require.NoError(t, err)
	require.Equal(t, "1", marker)
	require.False(t, second.TryConsumeSession("new-wire"))
	require.NoError(t, client.Del(ctx, "oauth:session:xai:new-wire").Err())
	_, ok = first.Get("new-wire")
	require.False(t, ok, "远端删除不能复活本地副本")
}
