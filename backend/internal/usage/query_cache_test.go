package usage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Concurrent statistics reads can share a query while the first caller
// cancels its own wait. The blocking repository announces entry and waits for
// an explicit release or shared-context cancellation, and the second context
// announces when querycache reaches its cancellation select.
//
// The test orders second-waiter registration before canceling the first
// caller, then releases the one repository read. Its observations distinguish
// cancellation of one HTTP-style waiter from cancellation of work still
// needed by another. Separate key cases check repeatability and the model,
// user, team and endpoint dimensions exercised here.

// statsCancellationRepo blocks shared statistics queries until the test
// releases or cancels them.
type statsCancellationRepo struct {
	UsageLogRepository // unused usage repository method slots
	// started closes when calls reaches one, and the test closes release to
	// finish the shared query. Configure both before dispatch and observe calls
	// atomically across waiters.
	started chan struct{} // first loader entry signal
	release chan struct{} // explicit shared loader completion signal
	calls   atomic.Int32  // atomic shared loader invocation count
}

// joinedStatsContext observes querycache waiter registration through the
// context's Done lookup.
type joinedStatsContext struct {
	context.Context // embedded live waiter context
	// joined closes when Done first announces a cancellation-channel check.
	// once prevents repeated Done calls from closing that publication channel
	// again while returning the embedded channel.
	joined chan struct{} // waiter cancellation check signal
	once   sync.Once     // single joined signal publication guard
}

// TestUsageStatsCacheKey_StableAndDistinct repeats one filter set and varies
// model, user, team and endpoint source. It requires stability and distinct
// keys for the exercised variations.
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

	withTeam := base
	withTeam.TeamID = 11
	require.NotEqual(t, k1, usageStatsCacheKey(withTeam), "different team must change key")
	keys := map[string]bool{k1: true}
	for _, source := range []string{"inbound", "upstream", "path"} {
		filtered := base
		filtered.EndpointSource = source
		key := usageStatsCacheKey(filtered)
		require.False(t, keys[key])
		keys[key] = true
	}
}

// GetStatsWithFilters announces the first repository call and waits for
// release or shared-context cancellation. The fixture counter records how
// many loaders actually reach this method.
//
// Release supplies the fixed statistics result, while canceled shared work
// returns its context error. This select lets the test determine whether
// canceling the first cache waiter incorrectly cancels a loader still needed
// by another waiter. Callers configure channels before dispatch and wait for
// completion before changing them.
func (r *statsCancellationRepo) GetStatsWithFilters(ctx context.Context, _ UsageLogFilters) (*UsageStats, error) {
	// Publish initial entry before waiting so the test can add another cache
	// waiter to a known active load before canceling its first caller.
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

// Done announces once that querycache has reached this waiter's
// cancellation-channel check. It returns the embedded context channel,
// allowing the test to order leader cancellation after the live waiter
// registered.
func (c *joinedStatsContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

// TestStatsCanceledLeaderKeepsLiveWaiter registers a second waiter before
// canceling the first. It requires first-caller cancellation, the second
// caller's statistics result and one repository load.
func TestStatsCanceledLeaderKeepsLiveWaiter(t *testing.T) {
	repo := &statsCancellationRepo{started: make(chan struct{}), release: make(chan struct{})}
	service := NewUsageService(repo)
	filters := UsageLogFilters{EndpointSource: "upstream"}
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan error, 1)
	// Enter the repository before adding the second waiter so both requests join
	// the same active flight and the fixture can observe one shared query invocation.
	go func() { _, _, err := service.GetStatsCached(first, filters); firstDone <- err }()
	<-repo.started
	live := &joinedStatsContext{Context: context.Background(), joined: make(chan struct{})}
	nextDone := make(chan error, 1)
	nextValue := make(chan *UsageStats, 1)
	go func() { value, _, err := service.GetStatsCached(live, filters); nextValue <- value; nextDone <- err }()
	// Done is queried after cache registration. Wait for that observation before
	// canceling the leader, leaving the second waiter active when shared work is evaluated.
	<-live.joined
	cancel()
	require.ErrorIs(t, <-firstDone, context.Canceled)
	// Complete shared loading after the first cancellation has returned. The live
	// waiter's result then demonstrates that its needed repository work remained available.
	close(repo.release)
	require.NoError(t, <-nextDone)
	require.EqualValues(t, 17, (<-nextValue).TotalRequests)
	require.EqualValues(t, 1, repo.calls.Load())
}
