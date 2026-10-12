package usage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// statsCancellationRepo blocks reads so the test can interleave cancellation of two requests.
type statsCancellationRepo struct {
	UsageLogRepository
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

// joinedStatsContext signals when a waiter checks its cancellation channel.
type joinedStatsContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func TestUsageStatsCacheKey_StableAndDistinct(t *testing.T) {
	start := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	base := UsageLogFilters{StartTime: &start, EndTime: &end, Model: "claude-3"}

	k1 := usageStatsCacheKey(base)
	k2 := usageStatsCacheKey(base)
	require.NotEmpty(t, k1)
	require.Equal(t, k1, k2, "same filters must produce same key")

	other := base
	other.Model = "gpt-4o"
	require.NotEqual(t, k1, usageStatsCacheKey(other), "different model must change key")

	withUser := base
	withUser.UserID = 7
	require.NotEqual(t, k1, usageStatsCacheKey(withUser), "different user must change key")

	// Team IDs keep one team's cached summary from being reused for another.
	withTeam := base
	withTeam.TeamID = 11
	require.NotEqual(t, k1, usageStatsCacheKey(withTeam), "different team must change key")
	// Each endpoint dimension has a distinct cache entry for the same range.
	keys := map[string]bool{k1: true}
	for _, source := range []string{"inbound", "upstream", "path"} {
		filtered := base
		filtered.EndpointSource = source
		key := usageStatsCacheKey(filtered)
		require.False(t, keys[key])
		keys[key] = true
	}
}

// GetStatsWithFilters waits for release or cancellation of the shared query context.
func (r *statsCancellationRepo) GetStatsWithFilters(ctx context.Context, _ UsageLogFilters) (*UsageStats, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
	}
	select {
	case <-r.release:
		return &UsageStats{TotalRequests: 17}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Done signals the test when the waiter checks for cancellation.
func (c *joinedStatsContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

// TestStatsCanceledLeaderKeepsLiveWaiter checks that canceling the first request leaves another waiter active.
func TestStatsCanceledLeaderKeepsLiveWaiter(t *testing.T) {
	repo := &statsCancellationRepo{started: make(chan struct{}), release: make(chan struct{})}
	service := NewUsageService(repo)
	filters := UsageLogFilters{EndpointSource: "upstream"}
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { _, _, err := service.GetStatsCached(first, filters); firstDone <- err }()
	<-repo.started
	live := &joinedStatsContext{Context: context.Background(), joined: make(chan struct{})}
	nextDone := make(chan error, 1)
	nextValue := make(chan *UsageStats, 1)
	go func() { value, _, err := service.GetStatsCached(live, filters); nextValue <- value; nextDone <- err }()
	<-live.joined
	cancel()
	require.ErrorIs(t, <-firstDone, context.Canceled)
	close(repo.release)
	require.NoError(t, <-nextDone)
	require.EqualValues(t, 17, (<-nextValue).TotalRequests)
	require.EqualValues(t, 1, repo.calls.Load())
}
