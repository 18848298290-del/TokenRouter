//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	keyredis "github.com/TokenFlux/TokenRouter/internal/apikey/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Two API Key services share Redis authentication snapshots while maintaining
// their own in-process reads. The PostgreSQL fixture updates a key name,
// removes its shared cached snapshot, and publishes invalidation so both
// services eventually reload the stored name. A selected model-mapping mutation
// checks that one returned request view does not alter later reads.
//
// The test observes two Redis subscribers before publishing, then kills those
// subscriber connections in its isolated Redis container and waits for both to
// reconnect. A second publication checks refreshed reads after reconnection.
// Services are stopped before Redis is probed again, and subscriber count
// reaches zero; a read through the stopped service returns the
// authentication-stopped error.

// TestAuthPubSubReconnect warms two authentication services sharing Redis,
// mutates a returned mapping, and disconnects subscribers. Reads retain the
// stored mapping, invalidations work after reconnection, and stopped
// authentication is rejected.
func TestAuthPubSubReconnect(t *testing.T) {
	integrationDB, integrationEntClient := identityDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	client := integrationEntClient
	rdb := rediscontainer.New(t)
	user := mustCreateUser(t, client, &identity.User{})
	store := keypostgres.NewKeyStore(client, integrationDB, nil)
	key := &apikey.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-test-pubsub-%d", time.Now().UnixNano()), Name: "before", Status: "active", ModelMapping: map[string]string{"client": "upstream"}}
	require.NoError(t, store.Create(ctx, key))
	cache := keyredis.NewAPIKeyCache(rdb)
	options := &apikey.Options{APIKeyAuth: apikey.APIKeyAuthCacheConfig{L1Size: 1024, L1TTLSeconds: 60, L2TTLSeconds: 60, Singleflight: true}}
	one := apikey.NewAPIKeyService(store, identitypostgres.NewUserStore(client, integrationDB), nil, nil, nil, cache, options)
	two := apikey.NewAPIKeyService(store, identitypostgres.NewUserStore(client, integrationDB), nil, nil, nil, cache, options)
	// Register both service stops after Redis fixture acquisition so their
	// callbacks stop subscribers before testing closes Redis; explicit StopContext
	// calls are also exercised.
	t.Cleanup(one.Stop)
	t.Cleanup(two.Stop)
	one.Start()
	two.Start()
	subscribers := func() int64 {
		v, e := rdb.PubSubNumSub(ctx, keyredis.AuthCacheInvalidateChannel).Result()
		// Treat introspection errors as an unready observation so subscriber polling
		// can retry until its bounded wait ends instead of accepting a failed count.
		if e != nil {
			return -1
		}
		return v[keyredis.AuthCacheInvalidateChannel]
	}
	require.Eventually(t, func() bool { return subscribers() == 2 }, 5*time.Second, 10*time.Millisecond)
	for _, core := range []*apikey.APIKeyService{one, two} {
		v, e := core.GetByKey(ctx, key.Key)
		require.NoError(t, e)
		require.Equal(t, "before", v.Name)
		v.ModelMapping["client"] = "request-only"
	}
	for _, core := range []*apikey.APIKeyService{one, two} {
		v, e := core.GetByKey(ctx, key.Key)
		require.NoError(t, e)
		require.Equal(t, "upstream", v.ModelMapping["client"])
	}
	cacheKey := one.KeyAuthCacheKey(key.Key)
	// Persist each name before deleting the shared snapshot and broadcasting its
	// cache key so both subscribers can reload the committed update during
	// polling.
	publish := func(name string) {
		_, e := client.APIKey.UpdateOneID(key.ID).SetName(name).Save(ctx)
		require.NoError(t, e)
		require.NoError(t, cache.DeleteAuthCache(ctx, cacheKey))
		require.NoError(t, cache.PublishAuthCacheInvalidation(ctx, cacheKey))
		require.Eventually(t, func() bool {
			for _, core := range []*apikey.APIKeyService{one, two} {
				v, e := core.GetByKey(ctx, key.Key)
				if e != nil || v.Name != name {
					return false
				}
			}
			return true
		}, 5*time.Second, 10*time.Millisecond)
	}
	publish("after-publish")
	// Kill the two subscriber connections in this isolated Redis container so
	// reconnection can be observed before sending the next invalidation to both
	// services.
	killed, e := rdb.ClientKillByFilter(ctx, "TYPE", "pubsub").Result()
	require.NoError(t, e)
	require.Equal(t, int64(2), killed)
	require.Eventually(t, func() bool { return subscribers() == 2 }, 8*time.Second, 10*time.Millisecond)
	publish("after-reconnect")
	// Stop subscriber work while Redis remains available, then confirm the channel
	// is empty and a direct Redis probe still succeeds before container cleanup
	// runs.
	stopCtx, stopCancel := context.WithTimeout(ctx, 2*time.Second)
	defer stopCancel()
	require.NoError(t, one.StopContext(stopCtx))
	require.NoError(t, two.StopContext(stopCtx))
	require.Eventually(t, func() bool { return subscribers() == 0 }, time.Second, 10*time.Millisecond)
	require.NoError(t, rdb.Ping(ctx).Err(), "认证资源先于 Redis 关闭")
	_, e = one.GetByKey(ctx, key.Key)
	require.ErrorIs(t, e, apikey.ErrAuthenticationStopped)
}
