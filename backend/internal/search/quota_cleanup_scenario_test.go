package search

// These scenarios connect search cancellation and quota release with shutdown waiting.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// blockedQuotaCleanup waits for the cleanup deadline and records its cancellation error.
type blockedQuotaCleanup struct {
	quotaFixture
	entered chan context.Context
	err     error
}

type canceledSearchExecutor struct {
	noSearchExecutor
	cancel context.CancelFunc
}

func TestQuotaCleanupBudgetAndShutdownWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	quota := &blockedQuotaCleanup{entered: make(chan context.Context, 1)}
	work := NewWorkGroup()
	manager := NewManager([]ProviderConfig{{Type: ProviderTypeBrave, APIKey: "fixture", QuotaLimit: 10}}, quota, canceledSearchExecutor{cancel: cancel}, work)
	result := make(chan error, 1)
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

func (q *blockedQuotaCleanup) Decrement(ctx context.Context, _ string) error {
	q.entered <- ctx
	<-ctx.Done()
	q.err = ctx.Err()
	return q.err
}

func (e canceledSearchExecutor) Search(ctx context.Context, _ ProviderConfig, _ SearchRequest) (*SearchResponse, error) {
	e.cancel()
	return nil, ctx.Err()
}
