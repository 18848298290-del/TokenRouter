package batchimage

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// Cleanup may be cancelled immediately after its runtime starts. The private
// repository fixture returns cancellation errors from both scan methods,
// exercising loop shutdown on a single processor while Start and Stop occur
// back to back.

// cleanupRepo supplies cancellation-aware scan responses for the immediate
// cleanup shutdown test.
type cleanupRepo struct {
	BatchImageRepository // unused repository method dependencies
}

// TestCleanupImmediateStop starts cleanup and immediately stops its runtime
// with one processor available. Returning from Stop exercises cancellation
// and completion waiting for a loop that may begin after shutdown.
func TestCleanupImmediateStop(t *testing.T) {
	// A single processor widens the ordering where Stop cancels before the
	// cleanup goroutine first runs; restore the prior setting after the test
	// finishes.
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	core := &Cleanup{Repo: &cleanupRepo{}}
	s := NewRuntime("batch image cleanup", true, core.Run)
	s.Start()
	s.Stop()
}

// ListBatchImageJobsDueForInputCleanup returns no candidates and the caller's
// context error. The immediate-stop fixture lets cleanup reach its
// cancellation path without accessing other embedded repository methods
// during runtime shutdown.
func (*cleanupRepo) ListBatchImageJobsDueForInputCleanup(ctx context.Context, _ time.Time, _ int) ([]*BatchImageJob, error) {
	return nil, ctx.Err()
}

// ListBatchImageJobsDueForOutputCleanup returns no candidates and the
// caller's context error. This fixture completes the output scan path if
// cleanup starts before its runtime cancellation becomes observable.
func (*cleanupRepo) ListBatchImageJobsDueForOutputCleanup(ctx context.Context, _ time.Time, _ int) ([]*BatchImageJob, error) {
	return nil, ctx.Err()
}
