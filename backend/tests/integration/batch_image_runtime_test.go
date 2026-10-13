package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchredis "github.com/TokenFlux/TokenRouter/internal/batchimage/rediscache"
)

// An empty batch-image queue provides no job for the runtime to process at
// startup. The test wires reserve, delayed-move and stale-recovery loops to
// miniredis, then inspects keys after stopping the runtime; it observes startup
// state without a queue-call barrier.

// noopBatchImageProcessor supplies an empty processing result for the idle
// queue fixture.
type noopBatchImageProcessor struct{}

// TestBatchImageWorkerRuntime_StartupDoesNotCreateRedisBatchImageKeys starts
// three worker loops against empty miniredis and waits for the runtime running
// flag. After Stop joins those loops, no key may use the configured batch_image
// prefix.
func TestBatchImageWorkerRuntime_StartupDoesNotCreateRedisBatchImageKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		// Normal flow stops the runtime before client cleanup. This
		// cleanup also closes Redis connections after an early
		// assertion exit, with close errors discarded by the fixture.
		_ = rdb.Close()
	})

	queue := batchredis.NewBatchImageQueue(rdb, &batchredis.QueueOptions{
		ReadyKey: "batch_image:queue:ready", DelayedKey: "batch_image:queue:delayed",
		ActiveKey: "batch_image:queue:active", InflightPrefix: "batch_image:queue:inflight:",
		LockPrefix: "batch_image:queue:lock:", InflightTTL: time.Minute, LockTTL: time.Minute,
	})
	worker := batchimage.NewBatchImageWorker(queue, noopBatchImageProcessor{}, batchimage.BatchImageWorkerOptions{
		JobLockTTL: time.Minute, DelayedPollInterval: time.Minute,
		RecoveryInterval: time.Minute, StaleActiveAfter: time.Minute,
		DelayedMoveLimit: 10, RecoverLimit: 10,
	})
	// The runtime omits BillingRecovery so the observed key set comes from
	// these three queue loops, with no database-backed funding recovery
	// participant.
	runtime := batchimage.NewRuntime("batch image worker", true, worker.Run, worker.RunDelayedMover, worker.RunStaleActiveRecovery)

	runtime.Start()
	// This one-second wait observes the installed runtime cancellation
	// state; Running can become true before any worker loop performs its
	// first queue operation.
	require.Eventually(t, runtime.Running, time.Second, 10*time.Millisecond)
	// Join the cancelled loop goroutines before reading miniredis keys so
	// the assertion cannot race a loop still performing its final Redis
	// operation.
	runtime.Stop()

	for _, key := range mr.Keys() {
		require.False(t, strings.HasPrefix(key, "batch_image:"), "unexpected Redis key created at startup: %s", key)
	}
}

// Process returns a nonterminal empty result if the fixture receives a job. The
// startup test enqueues no work, so it checks key creation rather than
// processing or settlement.
func (noopBatchImageProcessor) Process(context.Context, string) (batchimage.BatchImageProcessResult, error) {
	return batchimage.BatchImageProcessResult{}, nil
}
