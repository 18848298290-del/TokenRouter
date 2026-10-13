package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// A user's download counter rejects a second request when the configured
// capacity is one. The test observes expiry after acquisition and deletion after
// release, then checks that the freed capacity admits another request.

// TestBatchImageDownloadLimiter_AcquireDenyReleaseAndTTL exercises a one-slot
// user counter. Acquisition sets expiry, a second request is denied, repeated
// release leaves the counter absent, and a later acquisition succeeds.
func TestBatchImageDownloadLimiter_AcquireDenyReleaseAndTTL(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// Close the client after the test completes while RunT manages the Redis
	// fixture lifetime. Cleanup errors are discarded because assertions exercise
	// counter behavior rather than client shutdown.
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := &batchImageDownloadLimiter{
		rdb:          rdb,
		activePrefix: defaultBatchImageDownloadActivePrefix,
		maxActive:    1,
		ttl:          time.Minute,
	}

	permit, err := limiter.Acquire(ctx, "11", "zip")
	require.NoError(t, err)
	require.NotNil(t, permit)
	require.True(t, mr.TTL(limiter.activeKey("11")) > 0)

	_, err = limiter.Acquire(ctx, "11", "zip")
	require.ErrorIs(t, err, batchimage.ErrBatchImageDownloadLimited)

	require.NoError(t, permit.Release(ctx))
	require.NoError(t, permit.Release(ctx))
	require.False(t, mr.Exists(limiter.activeKey("11")))

	permit, err = limiter.Acquire(ctx, "11", "zip")
	require.NoError(t, err)
	require.NotNil(t, permit)
}
