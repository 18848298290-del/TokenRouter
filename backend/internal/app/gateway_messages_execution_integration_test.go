//go:build integration

package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	gatewaypg "github.com/TokenFlux/TokenRouter/internal/gateway/postgres"
	gatewayredis "github.com/TokenFlux/TokenRouter/internal/gateway/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Error-rule updates propagate through PostgreSQL and Redis subscriptions to
// another runtime instance. The scenario checks compatible cache keys and TTL,
// returned-snapshot isolation, failed persistence, successful disable
// publication, and deletion through the same persistent rule service.

// TestErrorRulesStorageAndSubscription starts two error-rule subscribers over
// migrated PostgreSQL and Redis. It checks cross-instance publication, cache
// representation and expiry, isolated returned data, unchanged published rules
// after failed writes, and stored disable/delete results.
func TestErrorRulesStorageAndSubscription(t *testing.T) {
	f := newDatabaseFixture(t)
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
	addr, err := container.Endpoint(ctx, "")
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	repo := gatewaypg.NewErrorPassthroughRepository(f.client)
	a := errorpolicy.NewErrorPassthroughService(repo, gatewayredis.NewErrorPassthroughCache(rdb))
	b := errorpolicy.NewErrorPassthroughService(repo, gatewayredis.NewErrorPassthroughCache(rdb))
	// Cleanup runs in reverse registration order. Stop both subscribers before
	// closing the Redis client and container so final subscription callbacks retain their dependencies.
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		require.NoError(t, a.StopContext(stop))
		require.NoError(t, b.StopContext(stop))
	})
	require.NoError(t, a.StartContext(ctx))
	require.NoError(t, b.StartContext(ctx))
	// Subscription setup completes asynchronously. Wait for both registrations
	// before publishing so the cross-instance assertion observes the active listener path.
	require.Eventually(t, func() bool {
		counts, e := rdb.PubSubNumSub(ctx, "error_passthrough_rules_updated").Result()
		return e == nil && counts["error_passthrough_rules_updated"] == 2
	}, 2*time.Second, 10*time.Millisecond)
	rule, err := a.Create(ctx, &errorpolicy.ErrorPassthroughRule{Name: "test", Enabled: true, MatchMode: errorpolicy.MatchModeAny, ErrorCodes: []int{503}, PassthroughCode: true, PassthroughBody: true, Platforms: []string{"openai"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return b.MatchRule("openai", 503, nil) != nil }, time.Second, 10*time.Millisecond)
	raw, err := rdb.Get(ctx, "error_passthrough_rules").Bytes()
	require.NoError(t, err)
	var cached []*errorpolicy.ErrorPassthroughRule
	require.NoError(t, json.Unmarshal(raw, &cached))
	require.Len(t, cached, 1)
	require.Equal(t, rule.ID, cached[0].ID)
	ttl, err := rdb.TTL(ctx, "error_passthrough_rules").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 23*time.Hour)
	require.LessOrEqual(t, ttl, 24*time.Hour)
	t.Run("returned-snapshot-isolation", func(t *testing.T) {
		snapshot := b.MatchRule("openai", 503, nil)
		snapshot.Enabled = false
		snapshot.Platforms[0] = "other"
		require.NotNil(t, b.MatchRule("openai", 503, nil))
	})
	t.Run("failed-write-preserves-published-rules", func(t *testing.T) {
		invalid := *rule
		invalid.ID = rule.ID + 1000
		invalid.Enabled = false
		_, e := a.Update(ctx, &invalid)
		require.Error(t, e)
		require.NotNil(t, a.MatchRule("openai", 503, nil))
		require.NotNil(t, b.MatchRule("openai", 503, nil))
	})
	t.Run("disable-publishes-after-storage", func(t *testing.T) {
		rule.Enabled = false
		_, e := a.Update(ctx, rule)
		require.NoError(t, e)
		require.Nil(t, a.MatchRule("openai", 503, nil))
		require.Eventually(t, func() bool { return b.MatchRule("openai", 503, nil) == nil }, time.Second, 10*time.Millisecond)
		stored, e := repo.GetByID(ctx, rule.ID)
		require.NoError(t, e)
		require.False(t, stored.Enabled)
	})
	t.Run("delete-publishes-empty-collection", func(t *testing.T) {
		require.NoError(t, a.Delete(ctx, rule.ID))
		rules, e := a.List(ctx)
		require.NoError(t, e)
		require.Empty(t, rules)
	})
}
