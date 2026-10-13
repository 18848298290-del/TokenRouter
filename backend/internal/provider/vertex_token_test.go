package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// vertexCacheFixture scripts token-cache operations and atomically captures
// their invocation counts.
type vertexCacheFixture struct {
	AccessTokenCache              // unimplemented token-cache fixture methods
	reads            atomic.Int32 // token read invocation count
	// get is required by token reads and receives their incremented ordinal.
	// Configure it before acquisition to script miss and peer outcomes.
	get func(context.Context, int32) (string, error)
	// lock is required by admission and supplies its scripted outcome. Tests can
	// cancel the caller from this callback before the contention wait.
	lock func(context.Context) (bool, error)
	// set is required when exchange succeeds and cache publication runs. Failed
	// exchange scenarios can leave it unset because publication is skipped.
	set      func(string, time.Duration)
	releases atomic.Int32 // refresh lock release count
}

// GetAccessToken increments the read counter and invokes the required get
// callback. Tests configure that callback before acquisition and use its
// ordinal to script a peer token.
func (c *vertexCacheFixture) GetAccessToken(ctx context.Context, _ string) (string, error) {
	return c.get(ctx, c.reads.Add(1))
}

// AcquireRefreshLock invokes the required lock callback and ignores key and
// TTL. The fixture records admission outcomes through that callback rather
// than retaining lock ownership state.
func (c *vertexCacheFixture) AcquireRefreshLock(ctx context.Context, _ string, ttl time.Duration) (bool, error) {
	return c.lock(ctx)
}

// ReleaseRefreshLock atomically counts cleanup and returns success for the
// fixture.
func (c *vertexCacheFixture) ReleaseRefreshLock(context.Context, string) error {
	c.releases.Add(1)
	return nil
}

// SetAccessToken forwards the token and TTL to the required set callback. It
// ignores context and cache key, letting successful-exchange tests assert the
// requested publication values.
func (c *vertexCacheFixture) SetAccessToken(_ context.Context, _ string, token string, ttl time.Duration) error {
	c.set(token, ttl)
	return nil
}

// TestVertexTokenLockCancellationStopsReadsAndExchange cancels during rejected
// lock admission. Acquisition must return cancellation after its first cache
// read, with no exchange or unowned lock release.
func TestVertexTokenLockCancellationStopsReadsAndExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &vertexCacheFixture{get: func(context.Context, int32) (string, error) { return "", nil }, lock: func(context.Context) (bool, error) { cancel(); return false, nil }}
	var exchanges atomic.Int32
	token, err := GetVertexServiceAccountAccessToken(ctx, VertexTokenOptions{Cache: c, CacheKey: "fixture", Exchange: func(context.Context) (string, time.Duration, error) {
		exchanges.Add(1)
		return "unexpected", time.Hour, nil
	}})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, token)
	require.EqualValues(t, 1, c.reads.Load())
	require.Zero(t, exchanges.Load())
	require.Zero(t, c.releases.Load())
}

// TestVertexTokenPeerWaitAndOwnedRelease compares a peer token, owned-lock
// exchange failure, and cache outage. The peer waits and skips release, owned
// failure releases once, and outage exchanges and publishes the supplied TTL.
func TestVertexTokenPeerWaitAndOwnedRelease(t *testing.T) {
	t.Run("peer", func(t *testing.T) {
		var lockTime time.Time
		c := &vertexCacheFixture{get: func(_ context.Context, n int32) (string, error) {
			if n == 1 {
				return "", nil
			}
			return "peer", nil
		}, lock: func(context.Context) (bool, error) { lockTime = time.Now(); return false, nil }}
		token, err := GetVertexServiceAccountAccessToken(context.Background(), VertexTokenOptions{Cache: c, CacheKey: "fixture", Exchange: func(context.Context) (string, time.Duration, error) { t.Fatal("不应重复交换"); return "", 0, nil }})
		require.NoError(t, err)
		require.Equal(t, "peer", token)
		require.GreaterOrEqual(t, time.Since(lockTime), VertexLockWaitTime)
		require.Zero(t, c.releases.Load())
	})
	t.Run("owned", func(t *testing.T) {
		c := &vertexCacheFixture{get: func(context.Context, int32) (string, error) { return "", nil }, lock: func(context.Context) (bool, error) { return true, nil }}
		want := errors.New("exchange failed")
		_, err := GetVertexServiceAccountAccessToken(context.Background(), VertexTokenOptions{Cache: c, CacheKey: "fixture", Exchange: func(context.Context) (string, time.Duration, error) { return "", 0, want }})
		require.ErrorIs(t, err, want)
		require.EqualValues(t, 1, c.releases.Load())
	})
	t.Run("redis-failure", func(t *testing.T) {
		c := &vertexCacheFixture{get: func(context.Context, int32) (string, error) { return "", errors.New("redis unavailable") }, lock: func(context.Context) (bool, error) { return false, errors.New("redis unavailable") }, set: func(token string, ttl time.Duration) {
			require.Equal(t, "exchanged", token)
			require.Equal(t, 55*time.Minute, ttl)
		}}
		var warnings int
		token, err := GetVertexServiceAccountAccessToken(context.Background(), VertexTokenOptions{Cache: c, CacheKey: "fixture", Warn: func(string, ...any) { warnings++ }, Exchange: func(context.Context) (string, time.Duration, error) { return "exchanged", 55 * time.Minute, nil }})
		require.NoError(t, err)
		require.Equal(t, "exchanged", token)
		require.Equal(t, 1, warnings)
		require.Zero(t, c.releases.Load())
	})
}
