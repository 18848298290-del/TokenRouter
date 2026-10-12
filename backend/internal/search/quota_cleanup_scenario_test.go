package search

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A failed supplier attempt can leave quota cleanup running after its request
// has been canceled. The scenario uses a decrement fixture that waits on the
// cleanup context so shared shutdown observes that remaining work before the
// search call returns.
//
// The fixture records its deadline error after receiving an independent
// three-second budget. A shorter stop context times out while cleanup is
// blocked; joining the search result then permits a later stop to succeed.
// These assertions establish the supplied deadline and this context-aware
// fixture's completion, while live Redis socket timing follows the Redis
// client's own timeout options.

// blockedQuotaCleanup holds a decrement until its supplied cleanup context
// expires.
type blockedQuotaCleanup struct {
	quotaFixture // shared reservation counter fixture
	// entered publishes the cleanup context before the decrement waits for expiry.
	// Tests receive it before asserting stop cannot yet drain shared search work.
	entered chan context.Context
	// err records the observed cleanup-context error. Read it after the search
	// result is joined, which orders observation after Decrement has returned.
	err error
}

// canceledSearchExecutor cancels the parent request when its supplier attempt
// begins.
type canceledSearchExecutor struct {
	noSearchExecutor // inert remaining executor methods
	// cancel closes the request context before Search returns its error. Configure
	// it before Manager execution so the quota cleanup receives an already
	// canceled parent.
	cancel context.CancelFunc
}

// TestQuotaCleanupBudgetAndShutdownWait cancels execution after quota
// reservation and blocks its context-aware release fixture. The supplied
// cleanup deadline is independent, short stop waiting times out, and joining
// cleanup allows shutdown to drain.
func TestQuotaCleanupBudgetAndShutdownWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quota := &blockedQuotaCleanup{entered: make(chan context.Context, 1)}
	work := NewWorkGroup()
	manager := NewManager([]ProviderConfig{{Type: ProviderTypeBrave, APIKey: "fixture", QuotaLimit: 10}}, quota, canceledSearchExecutor{cancel: cancel}, work)
	result := make(chan error, 1)
	// Run the failed attempt independently so the test can inspect its blocked
	// cleanup context and request a stop before release finishes.
	go func() {
		_, _, err := manager.SearchWithBestProvider(ctx, SearchRequest{Query: "fixture"})
		result <- err
	}()
	cleanup := <-quota.entered
	require.NoError(t, cleanup.Err())
	deadline, ok := cleanup.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), 3*time.Second)
	stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stopCancel()
	require.ErrorIs(t, work.Stop(stop), context.DeadlineExceeded)
	// Join the search result before reading the fixture error and usage; this
	// orders assertions after the blocked decrement and WorkGroup completion.
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(4 * time.Second):
		t.Fatal("搜索额度清理未遵守三秒预算")
	}
	require.ErrorIs(t, quota.err, context.DeadlineExceeded)
	require.Equal(t, int64(1), quota.used)
	require.NoError(t, work.Stop(context.Background()))
}

// Decrement publishes its cleanup context through entered and waits for that
// context to finish. The fixture records and returns the deadline error while
// retaining the inherited reservation counter.
//
// Publishing before waiting lets the test inspect that independent context and
// request a short shared-work stop during cleanup. Search remains inside
// WorkGroup until this method returns, then sends its result; tests join that
// result before reading err. Completion establishes this fixture's
// context-aware timing, while Redis adapters use their client options for
// command I/O.
func (q *blockedQuotaCleanup) Decrement(ctx context.Context, _ string) error {
	// Publish the live detached context before waiting for its deadline so the
	// test can distinguish cleanup lifetime from parent-request cancellation.
	q.entered <- ctx
	<-ctx.Done()
	q.err = ctx.Err()
	return q.err
}

// Search cancels the request using the configured callback and returns ctx's
// error. The fixture drives Manager's failed-attempt release path without
// creating an HTTP exchange or classifying a proxy failure.
func (e canceledSearchExecutor) Search(ctx context.Context, _ ProviderConfig, _ SearchRequest) (*SearchResponse, error) {
	e.cancel()
	return nil, ctx.Err()
}
