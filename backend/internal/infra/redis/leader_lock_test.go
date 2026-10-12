package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Leader fixtures use one miniredis server to observe contention, release by
// the admitted owner, and admission after expiry. Replacing a key with a
// successor value models a late old release, requiring compare-and-delete to
// retain the successor's lock.

// newLeaderLockTestCache creates a miniredis server and its leader-lock
// client for t. Test cleanup closes the client and miniredis owns server
// cleanup; callers use the returned server to advance lease time.
func newLeaderLockTestCache(t *testing.T) (*LeaderLock, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &LeaderLock{rdb: rdb}, mr
}

// TestLeaderLockCache_AcquireContendedRelease has two owners contend for one
// job key. It requires the first owner to acquire, the second to be rejected,
// and the second to acquire after matching release.
func TestLeaderLockCache_AcquireContendedRelease(t *testing.T) {
	cache, _ := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "dashboard:aggregation:leader"

	ok, err := cache.TryAcquireLeaderLock(ctx, key, "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = cache.TryAcquireLeaderLock(ctx, key, "B", time.Minute)
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, cache.ReleaseLeaderLock(ctx, key, "A"))

	ok, err = cache.TryAcquireLeaderLock(ctx, key, "B", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
}

// TestLeaderLockCache_ReleaseIsCompareAndDelete replaces an admitted owner's
// value with a successor lease before release. It requires the delayed first
// release to succeed harmlessly while the successor value remains stored.
func TestLeaderLockCache_ReleaseIsCompareAndDelete(t *testing.T) {
	cache, _ := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "payment:order:expiry:leader"

	ok, err := cache.TryAcquireLeaderLock(ctx, key, "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Replace the lease to model a successor owning the same job when A's
	// delayed cleanup arrives; A's release must retain B's stored owner value.
	require.NoError(t, cache.rdb.Set(ctx, leaderLockKeyPrefix+key, "B", time.Minute).Err())

	require.NoError(t, cache.ReleaseLeaderLock(ctx, key, "A"))

	val, err := cache.rdb.Get(ctx, leaderLockKeyPrefix+key).Result()
	require.NoError(t, err)
	require.Equal(t, "B", val)
}

// TestLeaderLockCache_TTLExpires admits one owner with a one-minute lease and
// advances miniredis time two minutes. It requires another owner to acquire
// the job after that lease expires.
func TestLeaderLockCache_TTLExpires(t *testing.T) {
	cache, mr := newLeaderLockTestCache(t)
	ctx := context.Background()
	const key = "subscription:expiry:reminder:leader"

	ok, err := cache.TryAcquireLeaderLock(ctx, key, "A", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(2 * time.Minute)

	ok, err = cache.TryAcquireLeaderLock(ctx, key, "B", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
}
