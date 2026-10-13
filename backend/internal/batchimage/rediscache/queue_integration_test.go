//go:build integration

package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Replacing a Redis lock token models a worker losing ownership before its
// next operation. The integration cases check that renewal reports loss and an
// old holder's acknowledgement leaves the successor's active membership
// available for processing.

// TestBatchImageLostOwner replaces a holder's Redis lock with a successor
// acquisition. Renewal must return an error, and old-token acknowledgement
// must report lease loss while the successor's active member remains present.
func TestBatchImageLostOwner(t *testing.T) {
	t.Run("renewal_reports_loss", func(t *testing.T) {
		ctx := context.Background()
		db := rediscontainer.New(t)
		q := NewBatchImageQueue(db, nil)
		old, ok, err := q.TryAcquireJobLock(ctx, "imgbatch_test", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		// Delete the old lock before reacquisition to model expiry deterministically.
		// The old handle retains its token, so renewal tests ownership rather than
		// elapsed wall-clock time.
		require.NoError(t, db.Del(ctx, "batch_image:queue:lock:imgbatch_test").Err())
		next, ok, err := q.TryAcquireJobLock(ctx, "imgbatch_test", time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		defer func() { require.NoError(t, next.Release(ctx)) }()
		refresher, ok := old.(batchimage.BatchImageJobLockRefresher)
		require.True(t, ok)
		err = refresher.Refresh(ctx, time.Minute)
		require.Error(t, err, "旧 token 续期未命中，必须报告失去所有权")
	})
	t.Run("old_ack_preserves_successor", func(t *testing.T) {
		ctx := context.Background()
		db := rediscontainer.New(t)
		q := NewBatchImageQueue(db, nil)
		id := "imgbatch_test_ack"
		old, ok, err := q.TryAcquireJobLock(ctx, id, time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		defer func() { require.NoError(t, old.Release(ctx)) }()
		// Delete the old lock and acquire a new token before installing successor
		// activity. An acknowledgement through the old handle must reject that
		// ownership change before touching active membership.
		require.NoError(t, db.Del(ctx, "batch_image:queue:lock:"+id).Err())
		next, ok, err := q.TryAcquireJobLock(ctx, id, time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
		defer func() { require.NoError(t, next.Release(ctx)) }()
		require.NoError(t, db.ZAdd(ctx, "batch_image:queue:active", redis.Z{Score: float64(time.Now().UnixMilli()), Member: id}).Err())
		require.NoError(t, db.Set(ctx, "batch_image:queue:inflight:"+id, id, time.Hour).Err())
		require.ErrorIs(t, old.Ack(ctx), batchimage.ErrBatchImageLeaseLost)
		_, err = db.ZScore(ctx, "batch_image:queue:active", id).Result()
		require.NoError(t, err, "旧 worker 的 ACK 不能删除接管者的 active 记录")
	})
}
