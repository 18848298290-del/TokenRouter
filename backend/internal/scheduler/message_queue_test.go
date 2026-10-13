package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The interface assertion keeps the cleanup-worker fixture assignable to the
// complete message queue cache used by the service.
var _ UserMsgQueueCache = (*cleanupWorkerUserMsgQueueCache)(nil)

// cleanupWorkerUserMsgQueueCache counts reconciliation calls and accepts
// unused queue operations for worker tests.
type cleanupWorkerUserMsgQueueCache struct {
	// reconcileCalls counts worker visits and maxCount stores the last batch
	// limit. Each is an independent atomic observation while periodic work
	// progresses beside the polling test.
	reconcileCalls atomic.Int64 // expired lock reconciliation count
	maxCount       atomic.Int64 // latest reconciliation batch limit
}

// AcquireLock returns successful admission for the cleanup-worker fixture's
// unused lock path.
func (c *cleanupWorkerUserMsgQueueCache) AcquireLock(context.Context, int64, string, int) (bool, error) {
	return true, nil
}

// ReleaseLock returns successful release for the cleanup-worker fixture's
// unused lock path.
func (c *cleanupWorkerUserMsgQueueCache) ReleaseLock(context.Context, int64, string) (bool, error) {
	return true, nil
}

// GetLastCompletedMs returns zero as the fixture's absent completion-time
// observation.
func (c *cleanupWorkerUserMsgQueueCache) GetLastCompletedMs(context.Context, int64) (int64, error) {
	return 0, nil
}

// GetCurrentTimeMs returns the process clock in Unix milliseconds for fixture
// timing.
func (c *cleanupWorkerUserMsgQueueCache) GetCurrentTimeMs(context.Context) (int64, error) {
	return time.Now().UnixMilli(), nil
}

// ReconcileExpiredLockCandidates records the call and supplied batch limit
// using atomic fields. It reports one reclaimed candidate so worker tests can
// observe that reconciliation is invoked.
func (c *cleanupWorkerUserMsgQueueCache) ReconcileExpiredLockCandidates(_ context.Context, maxCount int) (int, error) {
	// The periodic worker writes while tests poll these observations.
	// Recording the call first lets a reader see it before the matching
	// batch-limit store becomes visible.
	c.reconcileCalls.Add(1)
	c.maxCount.Store(int64(maxCount))
	return 1, nil
}

// TestStartCleanupWorker_ReconcilesExpiredLockCandidates starts the queue
// service's periodic cleanup worker with an atomic recording cache. It waits
// for a reconciliation call and requires the worker's configured batch limit
// of one thousand.
func TestStartCleanupWorker_ReconcilesExpiredLockCandidates(t *testing.T) {
	cache := &cleanupWorkerUserMsgQueueCache{}
	svc := NewUserMessageQueueService(cache, nil, nil)
	defer svc.Stop()

	svc.StartCleanupWorker(time.Millisecond)

	require.Eventually(t, func() bool {
		return cache.reconcileCalls.Load() > 0
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1000, cache.maxCount.Load())
}
