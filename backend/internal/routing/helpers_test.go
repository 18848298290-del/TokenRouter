package routing

import (
	"context"
	"sync"
	"time"
)

// groupAvailabilityProbeRunnerRepoStub records claims and can block the first claim to test overlapping rounds.
type groupAvailabilityProbeRunnerRepoStub struct {
	mu           sync.Mutex
	claimCalls   int
	claimLimit   int
	claimStarted chan struct{}
	releaseClaim chan struct{}
	startedOnce  sync.Once
}

func (r *groupAvailabilityProbeRunnerRepoStub) ClaimDue(ctx context.Context, _ time.Time, _ time.Time, _ string, limit int) ([]GroupAvailabilityProbeDueGroup, error) {
	r.mu.Lock()
	r.claimCalls++
	r.claimLimit = limit
	r.mu.Unlock()

	if r.claimStarted != nil {
		r.startedOnce.Do(func() { close(r.claimStarted) })
	}
	if r.releaseClaim != nil {
		select {
		case <-r.releaseClaim:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}

func (r *groupAvailabilityProbeRunnerRepoStub) SaveResultAndScheduleNext(context.Context, *GroupAvailabilityProbeResult, time.Time) error {
	return nil
}

func (r *groupAvailabilityProbeRunnerRepoStub) GetSummaryByGroupIDs(context.Context, []int64, int, int, string, time.Time) (map[int64]*GroupAvailabilitySummary, error) {
	return nil, nil
}

func (r *groupAvailabilityProbeRunnerRepoStub) CleanupOldResults(context.Context, time.Time) error {
	return nil
}

func (r *groupAvailabilityProbeRunnerRepoStub) claimSnapshot() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimCalls, r.claimLimit
}
