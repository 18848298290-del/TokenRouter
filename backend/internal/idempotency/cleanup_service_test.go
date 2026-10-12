package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// idempotencyCleanupRepoStub records expiry calls while leaving command operations inert.
type idempotencyCleanupRepoStub struct {
	deleteCalls int   // observed expiry batch invocations
	lastLimit   int   // latest requested deletion limit
	deleteErr   error // injected expiry operation error
}

// blockingCleanupRepository holds cleanup open to test Stop waiting and repeated Start calls.
type blockingCleanupRepository struct {
	// IdempotencyRepository leaves command operations unimplemented. The cleanup
	// worker uses only the overridden DeleteExpired call in these lifecycle scenarios.
	IdempotencyRepository
	// entered closes when the first batch starts; release allows that batch to finish.
	// Their shared lifecycle lets tests keep deletion active while Stop waits for completion.
	entered, release chan struct{} // batch-entry notification and deletion release gates
}

// CreateProcessing returns no claim because this stub exercises only expiry cleanup.
func (r *idempotencyCleanupRepoStub) CreateProcessing(context.Context, *IdempotencyRecord) (bool, error) {
	return false, nil
}

// GetByScopeAndKeyHash returns no record because cleanup performs no command lookup.
func (r *idempotencyCleanupRepoStub) GetByScopeAndKeyHash(context.Context, string, string) (*IdempotencyRecord, error) {
	return nil, nil
}

// TryReclaim leaves ownership unchanged because these scenarios call only expiry deletion.
func (r *idempotencyCleanupRepoStub) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, nil
}

// ExtendProcessingLock returns false because cleanup tests renew no processing deadlines.
func (r *idempotencyCleanupRepoStub) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return false, nil
}

// MarkSucceeded succeeds inertly because cleanup scenarios store no callback outcomes.
func (r *idempotencyCleanupRepoStub) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return nil
}

// MarkFailedRetryable succeeds inertly because cleanup scenarios produce no retryable commands.
func (r *idempotencyCleanupRepoStub) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return nil
}

// DeleteExpired records the batch limit and invocation count for cleanup assertions.
// It returns the injected error when present; otherwise one synthetic deletion
// lets the worker exercise its successful cleanup reporting path.
func (r *idempotencyCleanupRepoStub) DeleteExpired(_ context.Context, _ time.Time, limit int) (int64, error) {
	r.deleteCalls++
	r.lastLimit = limit
	if r.deleteErr != nil {
		return 0, r.deleteErr
	}
	return 1, nil
}

// TestNewIdempotencyCleanupService_UsesConfig supplies a nondefault interval and
// batch size without starting workers. The constructed service must retain both
// values for its future scheduling and deletion rounds.
func TestNewIdempotencyCleanupService_UsesConfig(t *testing.T) {
	repo := &idempotencyCleanupRepoStub{}
	cfg := CleanupOptions{
		Interval: 7 * time.Second,
		Batch:    321,
	}
	svc := NewIdempotencyCleanupService(repo, cfg)
	require.Equal(t, 7*time.Second, svc.interval)
	require.Equal(t, 321, svc.batch)
}

// TestIdempotencyCleanupService_CleanupOnce invokes one expiry round with a chosen
// batch size. The repository must receive that limit exactly once, protecting the
// cleanup worker's bounded per-round deletion behavior.
func TestIdempotencyCleanupService_CleanupOnce(t *testing.T) {
	repo := &idempotencyCleanupRepoStub{}
	svc := NewIdempotencyCleanupService(repo, CleanupOptions{
		Batch: 99,
	})

	svc.cleanupOnce()
	require.Equal(t, 1, repo.deleteCalls)
	require.Equal(t, 99, repo.lastLimit)
}

// DeleteExpired signals the first batch and holds it until release or cancellation.
// Lifecycle tests call it once and use the release gate to observe Stop waiting;
// the worker's independent database context remains a bounded escape from the wait.
//
// Closing entered before selecting on release makes the in-flight deletion
// observable even while it cannot complete. A second worker would close entered
// again, exposing an unwanted duplicate Start through this same fixture.
func (r *blockingCleanupRepository) DeleteExpired(ctx context.Context, _ time.Time, limit int) (int64, error) {
	// Signal arrival before waiting so the test starts Stop while deletion is still
	// active; the release and context cases then determine when that batch can finish.
	close(r.entered)
	select {
	case <-r.release:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// TestCleanupStopWaitsForFirstRound repeats startup while the immediate deletion
// is blocked. Stop must wait for that deletion, then remain safe when stopped
// again or asked to start after its lifetime has ended.
func TestCleanupStopWaitsForFirstRound(t *testing.T) {
	repo := &blockingCleanupRepository{entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewIdempotencyCleanupService(repo, CleanupOptions{})
	require.Equal(t, 60*time.Second, svc.interval)
	require.Equal(t, 500, svc.batch)
	svc.Start()
	svc.Start()
	// Wait for the initial batch before starting shutdown, so an early Stop return
	// would demonstrate that the active deletion was omitted from worker lifetime accounting.
	<-repo.entered
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("回收尚未完成")
	case <-time.After(20 * time.Millisecond):
	}
	// Release the held database operation before waiting for shutdown completion,
	// demonstrating that Stop includes that operation instead of relying on its timeout to exit.
	close(repo.release)
	<-stopped
	svc.Stop()
	svc.Start()
}

// TestCleanupStopBeforeStart closes an inactive cleanup lifetime before invoking
// Start. The repository must receive no deletion calls, so a stopped service
// cannot launch its otherwise immediate initial cleanup afterward.
func TestCleanupStopBeforeStart(t *testing.T) {
	repo := &idempotencyCleanupRepoStub{}
	svc := NewIdempotencyCleanupService(repo, CleanupOptions{})
	svc.Stop()
	svc.Start()
	svc.Stop()
	require.Zero(t, repo.deleteCalls)
}
