package rediscache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// A bucket lock can expire while its original rebuilder is still finishing.
// Each acquisition writes a random owner token into the shared sched:v4 lock
// key with the caller's TTL, and release compares that token before deleting
// the key.
//
// The release script prevents an expired holder from removing a successor's
// lease. BucketLease carries the cleanup callback to the rebuild owner, and a
// missing or replaced token reports ErrBucketLeaseLost. This ownership check
// protects lock deletion; snapshot epoch checks live in SnapshotCache and
// decide whether a completed rebuild can publish its bucket version.

// releaseBucketOwner deletes a bucket lock when its stored token still
// matches the releasing owner, protecting a successor acquired after
// expiration.
var releaseBucketOwner = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

// BucketLocks acquires token-owned Redis leases for individual scheduler
// bucket rebuilds.
type BucketLocks struct {
	// client is borrowed from scheduler storage and stays open while any bucket
	// lease may invoke its release callback.
	client *redis.Client // shared Redis storage client
}

// NewBucketLocks constructs bucket lease storage over the supplied Redis
// client. The caller retains client ownership and keeps it usable through
// release of every acquired lease.
func NewBucketLocks(client *redis.Client) *BucketLocks { return &BucketLocks{client: client} }

// AcquireBucketLease attempts a token-owned lock and returns a BucketLease
// for successful acquisition. Use a positive TTL for an expiring lock; zero
// creates a persistent lock, and contention returns no lease.
//
// go-redis converts positive lifetimes to Redis expiry units, using at least
// one millisecond and discarding finer precision. The caller releases the
// returned lease after rebuilding; release compares the stored owner token.
func (s *BucketLocks) AcquireBucketLease(ctx context.Context, bucket scheduler.SchedulerBucket, ttl time.Duration) (*scheduler.BucketLease, bool, error) {
	// Sixteen random bytes form the stored hexadecimal owner identity.
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, false, err
	}
	owner := hex.EncodeToString(token[:])
	key := "sched:v4:lock:" + bucket.String()
	// Write a fresh random owner token with the lease lifetime so a later
	// release can identify this acquisition after expiration permits another
	// owner.
	acquired, err := s.client.SetNX(ctx, key, owner, ttl).Result()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	lease := scheduler.NewBucketLease(func(releaseCtx context.Context) error {
		// Compare the captured token within Redis before deletion so delayed
		// cleanup cannot release a bucket lock acquired by a successor.
		removed, err := releaseBucketOwner.Run(releaseCtx, s.client, []string{key}, owner).Int64()
		if err != nil {
			return err
		}
		// A zero deletion means this owner no longer holds the distributed lock;
		// report lease loss so rebuild cleanup can recognize expiration or
		// replacement.
		if removed == 0 {
			return scheduler.ErrBucketLeaseLost
		}
		return nil
	})
	return lease, true, nil
}
