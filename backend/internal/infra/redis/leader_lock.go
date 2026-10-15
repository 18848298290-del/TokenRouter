package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// leader_lock.go acquires Redis job leases and releases them only for the
// matching owner.
// Periodic jobs contend on a namespaced Redis key whose value identifies the
// admitted owner. SET NX chooses one claimant and the supplied TTL bounds its
// lease; a caller using a zero TTL creates a key that has no expiry. Jobs
// need a lease long enough for their protected work because this helper
// performs no renewal.
//
// An old job can finish after its lease expires and another job has claimed
// the same key. Release compares the stored owner with the supplied owner
// inside Lua before deleting, so a stale release cannot delete a differently
// identified successor. Callers use an owner value unique to each lease and
// retain responsibility for cancellation, runtime, and Redis-client cleanup.

// leaderLockKeyPrefix separates job-admission leases from the other Redis
// runtime keys while allowing callers to choose a distinct job identity.
const leaderLockKeyPrefix = "leader:lock:"

// leaderLockReleaseScript compares the owner before deleting the lock.
// A delayed release by an old owner must not delete a successor's lock.
var leaderLockReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

// LeaderLock delegates namespaced job leases and owner-checked release to Redis.
type LeaderLock struct {
	// rdb remains caller-owned and open while jobs acquire and release leases;
	// the adapter does not establish a separate client or shutdown lifecycle.
	rdb *redis.Client
}

// NewLeaderLockCache creates a leader-lock adapter using the caller's Redis
// client. Construction performs no network calls; callers retain client
// ownership and supply distinct job keys and lease-owner identities.
func NewLeaderLockCache(rdb *redis.Client) *LeaderLock {
	return &LeaderLock{rdb: rdb}
}

// TryAcquireLeaderLock stores owner under the prefixed key only when the key
// is absent. Use an owner unique to this lease and a positive ttl covering
// the job; zero ttl creates an unexpiring key and repeated calls do not renew
// an existing lease.
func (c *LeaderLock) TryAcquireLeaderLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, leaderLockKeyPrefix+key, owner, ttl).Result()
}

// ReleaseLeaderLock deletes the prefixed key when its stored owner equals
// owner, using one Lua operation. A mismatched or missing key is a successful
// no-op; Redis failures are returned for caller cleanup policy.
func (c *LeaderLock) ReleaseLeaderLock(ctx context.Context, key, owner string) error {
	return leaderLockReleaseScript.Run(ctx, c.rdb, []string{leaderLockKeyPrefix + key}, owner).Err()
}
