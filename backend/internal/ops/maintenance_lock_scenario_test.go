package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// leaderCacheFixture models sequential owner-specific lease claims for
// maintenance leadership tests.
type leaderCacheFixture struct {
	// owners is allocated before use and updated without synchronization or TTL
	// expiry. These scenarios invoke claims and releases sequentially.
	owners map[string]string // held key owner map
	// err overrides every Claim attempt when nonnil. Release continues comparing
	// stored ownership regardless of this injected claim error.
	err error // injected claim failure
}

// Claim records ownership for an unheld key or reports contention for an
// existing owner. A configured error takes precedence; context and TTL are
// ignored, so this fixture models no expiration.
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

// Release removes a held key when its stored owner matches the supplied owner.
// Mismatched owners and absent keys succeed without changes; context is ignored
// and fixture calls are sequential.
func (c *leaderCacheFixture) Release(_ context.Context, key, owner string) error {
	if c.owners[key] == owner {
		delete(c.owners, key)
	}
	return nil
}

// Get returns an empty successful cache value for these leadership-only
// fixtures.
func (*leaderCacheFixture) Get(context.Context, string) (string, error) { return "", nil }

// Put accepts cache writes without retaining values for these leadership-only
// fixtures.
func (*leaderCacheFixture) Put(context.Context, string, string, time.Duration) error { return nil }

// TestMaintenanceLeaderLockCompetition runs sequential cleanup and report
// contenders against one owner-tracking cache fixture. It checks a loser cannot
// release ownership, a successor can claim after release, and Redis failure
// with no database lock refuses execution.
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
			// Inject a Redis claim error while both job fixtures lack database locking,
			// exercising refusal rather than an unlocked fallback run.
			cache.err = errors.New("cache unavailable")
			release, ok = first(t.Context())
			require.False(t, ok)
			require.Nil(t, release)
		})
	}
}
