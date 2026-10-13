package rediscache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
)

// Queue tests observe Redis records after admission, reservation, retry
// movement, recovery, and holder operations. Controlled timestamps distinguish
// due or stale work from future or recently active jobs, while replacement
// tokens make ownership loss deterministic.

// TestBatchImageQueue_DuplicateEnqueueReturnsAlreadyQueued admits the same
// batch ID twice. The first admission succeeds, and the second returns the
// application error identifying an existing inflight marker.
func TestBatchImageQueue_DuplicateEnqueueReturnsAlreadyQueued(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	batchID := "imgbatch_duplicate"

	require.NoError(t, queue.Enqueue(ctx, batchID))
	err := queue.Enqueue(ctx, batchID)
	require.Error(t, err)
	require.True(t, errors.Is(err, batchimage.ErrBatchImageAlreadyQueued))
}

// TestBatchImageQueue_RequeueAfterMovesJobFromActiveToDelayed schedules a
// manually seeded active job one minute ahead. Requeue removes active membership
// and creates a delayed score greater than the current timestamp.
func TestBatchImageQueue_RequeueAfterMovesJobFromActiveToDelayed(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	batchID := "imgbatch_requeue_after"
	require.NoError(t, queue.rdb.ZAdd(ctx, queue.activeKey, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: batchID,
	}).Err())

	require.NoError(t, queue.RequeueAfter(ctx, batchID, time.Minute))
	require.ErrorIs(t, queue.rdb.ZScore(ctx, queue.activeKey, batchID).Err(), redis.Nil)
	score, err := queue.rdb.ZScore(ctx, queue.delayedKey, batchID).Result()
	require.NoError(t, err)
	require.Greater(t, score, float64(time.Now().UnixMilli()))
}

// TestBatchImageQueue_MoveDueDelayedToReadyMovesDueJobs seeds one due and one
// future delayed member. Only the due job moves, leaves delayed, and can be
// reserved from ready; the future member remains scheduled.
func TestBatchImageQueue_MoveDueDelayedToReadyMovesDueJobs(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	dueBatchID := "imgbatch_due"
	futureBatchID := "imgbatch_future"
	now := time.Now()
	require.NoError(t, queue.rdb.ZAdd(ctx, queue.delayedKey,
		redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: dueBatchID},
		redis.Z{Score: float64(now.Add(time.Hour).UnixMilli()), Member: futureBatchID},
	).Err())

	moved, err := queue.MoveDueDelayedToReady(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	require.ErrorIs(t, queue.rdb.ZScore(ctx, queue.delayedKey, dueBatchID).Err(), redis.Nil)
	require.NoError(t, queue.rdb.ZScore(ctx, queue.delayedKey, futureBatchID).Err())

	reserved, err := queue.Reserve(ctx, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, dueBatchID, reserved.BatchID)
}

// TestBatchImageQueue_RecoverStaleActiveMovesStaleJobsToReady seeds old and
// recent active timestamps. Recovery moves only the old member to ready,
// where reservation returns that batch ID while the recent member stays active.
func TestBatchImageQueue_RecoverStaleActiveMovesStaleJobsToReady(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	staleBatchID := "imgbatch_stale"
	recentBatchID := "imgbatch_recent"
	now := time.Now()
	require.NoError(t, queue.rdb.ZAdd(ctx, queue.activeKey,
		redis.Z{Score: float64(now.Add(-time.Hour).UnixMilli()), Member: staleBatchID},
		redis.Z{Score: float64(now.UnixMilli()), Member: recentBatchID},
	).Err())

	moved, err := queue.RecoverStaleActive(ctx, 10*time.Minute, 10)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	require.ErrorIs(t, queue.rdb.ZScore(ctx, queue.activeKey, staleBatchID).Err(), redis.Nil)
	require.NoError(t, queue.rdb.ZScore(ctx, queue.activeKey, recentBatchID).Err())

	reserved, err := queue.Reserve(ctx, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, staleBatchID, reserved.BatchID)
}

// TestBatchImageQueue_JobLockReleaseOnlyDeletesMatchingToken replaces a lock
// with another token before releasing the old holder. The replacement remains;
// a separately acquired matching holder can delete its own lock.
func TestBatchImageQueue_JobLockReleaseOnlyDeletesMatchingToken(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	batchID := "imgbatch_lock"

	lock, ok, err := queue.TryAcquireJobLock(ctx, batchID, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, queue.rdb.Set(ctx, queue.lockKey(batchID), "other-token", time.Minute).Err())
	require.NoError(t, lock.Release(ctx))
	got, err := queue.rdb.Get(ctx, queue.lockKey(batchID)).Result()
	require.NoError(t, err)
	require.Equal(t, "other-token", got)

	require.NoError(t, queue.rdb.Del(ctx, queue.lockKey(batchID)).Err())
	lock, ok, err = queue.TryAcquireJobLock(ctx, batchID, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, lock.Release(ctx))
	require.ErrorIs(t, queue.rdb.Get(ctx, queue.lockKey(batchID)).Err(), redis.Nil)
}

// TestBatchImageQueue_ReserveAtomicallyMovesJobToActive enqueues and reserves
// one job. It checks the returned ID, an empty ready list, and a positive
// active timestamp after reservation completes.
func TestBatchImageQueue_ReserveAtomicallyMovesJobToActive(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	batchID := "imgbatch_reserve"
	require.NoError(t, queue.Enqueue(ctx, batchID))

	reserved, err := queue.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, batchID, reserved.BatchID)

	// Check both queue locations after reservation. The ready entry has been
	// consumed, and its active timestamp gives stale recovery a record of the job
	// that the worker now holds.
	require.Equal(t, int64(0), queue.rdb.LLen(ctx, queue.readyKey).Val())
	score, err := queue.rdb.ZScore(ctx, queue.activeKey, batchID).Result()
	require.NoError(t, err)
	require.Positive(t, score)
}

// TestBatchImageQueue_ReserveReturnsEmptyAfterTimeout reserves from an empty
// queue with a short budget. The operation returns the queue-empty error within
// a generous upper time bound, protecting bounded idle waiting.
func TestBatchImageQueue_ReserveReturnsEmptyAfterTimeout(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)

	start := time.Now()
	_, err := queue.Reserve(ctx, 50*time.Millisecond)
	require.ErrorIs(t, err, batchimage.ErrBatchImageQueueEmpty)
	require.Less(t, time.Since(start), 5*time.Second)
}

// TestBatchImageQueue_ReserveDropsInvalidPayload inserts a malformed ready
// value directly. Reservation reports the payload error and removes its active
// member, preventing that tested value from entering stale recovery.
func TestBatchImageQueue_ReserveDropsInvalidPayload(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	require.NoError(t, queue.rdb.LPush(ctx, queue.readyKey, "not-a-batch-id").Err())

	_, err := queue.Reserve(ctx, 10*time.Millisecond)
	require.ErrorIs(t, err, batchimage.ErrInvalidBatchImageQueuePayload)
	// The reserve script first records the malformed value as active. The payload
	// validation path removes that record so this value cannot be repeatedly
	// reintroduced by stale recovery.
	require.ErrorIs(t, queue.rdb.ZScore(ctx, queue.activeKey, "not-a-batch-id").Err(), redis.Nil)
}

// TestBatchImageQueue_HeartbeatOnlyRefreshesExistingActiveMember exercises an
// unlocked job before and after active membership exists. Heartbeat leaves
// absence unchanged, then replaces the existing member's old timestamp.
func TestBatchImageQueue_HeartbeatOnlyRefreshesExistingActiveMember(t *testing.T) {
	ctx := context.Background()
	queue, _ := newBatchImageQueueTest(t)
	batchID := "imgbatch_heartbeat"

	// Start with no active member to exercise the XX update rule. A successful
	// unlocked heartbeat cannot create membership for work that was never reserved.
	require.NoError(t, queue.Heartbeat(ctx, batchID))
	require.ErrorIs(t, queue.rdb.ZScore(ctx, queue.activeKey, batchID).Err(), redis.Nil)

	require.NoError(t, queue.rdb.ZAdd(ctx, queue.activeKey, redis.Z{Score: 1, Member: batchID}).Err())
	require.NoError(t, queue.Heartbeat(ctx, batchID))
	score, err := queue.rdb.ZScore(ctx, queue.activeKey, batchID).Result()
	require.NoError(t, err)
	require.Greater(t, score, float64(1))
}

// TestBatchImageQueue_JobLockRefreshExtendsTTLOnlyForHolder renews a matching
// token and then replaces it. Matching renewal extends expiry, while the stale
// holder reports lease loss and leaves the replacement's shorter expiry intact.
func TestBatchImageQueue_JobLockRefreshExtendsTTLOnlyForHolder(t *testing.T) {
	ctx := context.Background()
	queue, mr := newBatchImageQueueTest(t)
	batchID := "imgbatch_lock_refresh"

	lock, ok, err := queue.TryAcquireJobLock(ctx, batchID, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	refresher, isRefresher := lock.(batchimage.BatchImageJobLockRefresher)
	require.True(t, isRefresher)

	require.NoError(t, refresher.Refresh(ctx, 10*time.Minute))
	ttl := mr.TTL(queue.lockKey(batchID))
	require.Greater(t, ttl, 5*time.Minute)

	// Replace the token directly to model ownership loss before renewal.
	// The old holder's longer TTL request must leave the replacement token's
	// existing expiry available for its actual owner.
	require.NoError(t, queue.rdb.Set(ctx, queue.lockKey(batchID), "other-token", time.Minute).Err())
	require.ErrorIs(t, refresher.Refresh(ctx, 10*time.Minute), batchimage.ErrBatchImageLeaseLost)
	ttl = mr.TTL(queue.lockKey(batchID))
	require.LessOrEqual(t, ttl, time.Minute)
}

// newBatchImageQueueTest constructs a queue over a test-owned Redis client.
// RunT manages the server, cleanup closes the client, and reduced inflight and
// lock budgets keep fixture expiry separate from production defaults.
func newBatchImageQueueTest(t *testing.T) (*batchImageQueue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// Close the client before the RunT server cleanup runs. Discard close errors
	// because queue assertions concern Lua results and record transitions, while
	// the fixture owns connection teardown.
	t.Cleanup(func() {
		_ = rdb.Close()
	})
	queue := newBatchImageQueueWithOptions(rdb, QueueOptions{
		InflightTTL: time.Hour,
		LockTTL:     time.Minute,
	})
	return queue, mr
}
