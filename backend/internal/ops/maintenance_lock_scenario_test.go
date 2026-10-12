package ops

// These scenarios cover leader-lock contention in cleanup and scheduled reports.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// leaderCacheFixture tracks ownership while two instances compete for the same maintenance job.
type leaderCacheFixture struct {
	owners map[string]string
	err    error
}

func (c *leaderCacheFixture) Claim(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	if _, exists := c.owners[key]; exists {
		return false, nil
	}
	c.owners[key] = owner
	return true, nil
}

func (c *leaderCacheFixture) Release(_ context.Context, key, owner string) error {
	if c.owners[key] == owner {
		delete(c.owners, key)
	}
	return nil
}

func (*leaderCacheFixture) Get(context.Context, string) (string, error)              { return "", nil }
func (*leaderCacheFixture) Put(context.Context, string, string, time.Duration) error { return nil }

// TestMaintenanceLeaderLockCompetition checks that a losing contender cannot release another instance's lock.
func TestMaintenanceLeaderLockCompetition(t *testing.T) {
	for _, task := range []string{"cleanup", "report"} {
		t.Run(task, func(t *testing.T) {
			cache := &leaderCacheFixture{owners: map[string]string{}}
			var first, second func(context.Context) (func(), bool)
			if task == "cleanup" {
				first = (&OpsCleanupService{redisClient: cache, instanceID: "first"}).tryAcquireLeaderLock
				second = (&OpsCleanupService{redisClient: cache, instanceID: "second"}).tryAcquireLeaderLock
			} else {
				first = NewOpsScheduledReportService(nil, nil, nil, cache, nil).tryAcquireLeaderLock
				second = NewOpsScheduledReportService(nil, nil, nil, cache, nil).tryAcquireLeaderLock
			}
			release, ok := first(t.Context())
			require.True(t, ok)
			require.NotNil(t, release)
			otherRelease, ok := second(t.Context())
			require.False(t, ok)
			require.Nil(t, otherRelease)
			require.Len(t, cache.owners, 1)
			release()
			release, ok = second(t.Context())
			require.True(t, ok)
			require.NotNil(t, release)
			release()
			require.Empty(t, cache.owners)
			// With Redis unavailable and no database lock, neither job can run.
			cache.err = errors.New("cache unavailable")
			release, ok = first(t.Context())
			require.False(t, ok)
			require.Nil(t, release)
		})
	}
}
