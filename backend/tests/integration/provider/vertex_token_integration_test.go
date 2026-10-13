//go:build integration

package provider_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providermodule "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Vertex service-account token requests may be canceled while another caller
// owns the refresh lock. A Redis-backed cache wrapper cancels immediately after
// lock acquisition is attempted, allowing the test to check read count, absence
// of exchange, peer-lock retention, and later cache reuse.

// vertexCancelAfterLockCache counts reads and cancels after the delegated Redis
// lock attempt.
type vertexCancelAfterLockCache struct {
	providermodule.AccessTokenCache // delegated Redis token cache
	// cancel ends the token operation after each delegated lock attempt, even
	// when acquisition fails. Tests configure it with that operation's CancelFunc.
	cancel context.CancelFunc
	reads  atomic.Int32 // delegated token-read call count
}

// GetAccessToken counts cache reads before delegating to the Redis-backed
// cache.
func (c *vertexCancelAfterLockCache) GetAccessToken(ctx context.Context, key string) (string, error) {
	c.reads.Add(1)
	return c.AccessTokenCache.GetAccessToken(ctx, key)
}

// AcquireRefreshLock delegates lock acquisition and cancels the operation
// before returning its result. Tests preinstall another owner's lock, so this
// callback checks cancellation after the unsuccessful lock attempt without
// claiming ownership.
func (c *vertexCancelAfterLockCache) AcquireRefreshLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	// Cancel before returning the attempted-lock result so the token helper
	// sees cancellation at its next decision while the peer's Redis lock remains held.
	locked, err := c.AccessTokenCache.AcquireRefreshLock(ctx, key, ttl)
	c.cancel()
	return locked, err
}

// TestVertexLockCancellationWithRedis cancels a token request immediately after
// it attempts a Redis lock already held by a peer. No exchange occurs, the peer
// lock remains, and a later cached token is returned.
func TestVertexLockCancellationWithRedis(t *testing.T) {
	cache := rediscache.NewOAuthTokenCache(rediscontainer.New(t))
	key := providermodule.VertexServiceAccountCacheKey(99, "test-vertex-fixture", t.Name(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Install a peer-held lock before requesting the token so the cancellation
	// case can detect an accidental exchange or release by a nonowner.
	locked, err := cache.AcquireRefreshLock(ctx, key, 30*time.Second)
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { require.NoError(t, cache.ReleaseRefreshLock(context.Background(), key)) }()
	canceled, stop := context.WithCancel(ctx)
	defer stop()
	tracked := &vertexCancelAfterLockCache{AccessTokenCache: cache, cancel: stop}
	var exchanges atomic.Int32
	token, err := providermodule.GetVertexServiceAccountAccessToken(canceled, providermodule.VertexTokenOptions{ProviderID: 99, CacheKey: key, Cache: tracked, Exchange: func(context.Context) (string, time.Duration, error) {
		exchanges.Add(1)
		return "unexpected", time.Hour, nil
	}})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, token)
	require.EqualValues(t, 1, tracked.reads.Load())
	require.Zero(t, exchanges.Load())
	locked, err = cache.AcquireRefreshLock(ctx, key, 30*time.Second)
	require.NoError(t, err)
	require.False(t, locked, "取消者不能释放其他持有者的锁")
	require.NoError(t, cache.SetAccessToken(ctx, key, "peer-token", 55*time.Minute))
	defer func() { require.NoError(t, cache.DeleteAccessToken(context.Background(), key)) }()
	// Leave the peer lock installed while supplying its token to Redis;
	// the subsequent request can succeed by reading cached state before exchange.
	token, err = providermodule.GetVertexServiceAccountAccessToken(ctx, providermodule.VertexTokenOptions{CacheKey: key, Cache: cache, Exchange: func(context.Context) (string, time.Duration, error) {
		exchanges.Add(1)
		return "unexpected", time.Hour, nil
	}})
	require.NoError(t, err)
	require.Equal(t, "peer-token", token)
	require.Zero(t, exchanges.Load())
}
