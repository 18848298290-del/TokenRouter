package rediscache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

var releaseBucketOwner = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

// BucketLocks stores scheduler bucket locks in Redis.
type BucketLocks struct{ client *redis.Client }

func NewBucketLocks(client *redis.Client) *BucketLocks { return &BucketLocks{client: client} }

// AcquireBucketLease acquires the bucket key with an owner token and TTL; release checks that token.
func (s *BucketLocks) AcquireBucketLease(ctx context.Context, bucket scheduler.SchedulerBucket, ttl time.Duration) (*scheduler.BucketLease, bool, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, false, err
	}
	owner := hex.EncodeToString(token[:])
	key := "sched:v4:lock:" + bucket.String()
	acquired, err := s.client.SetNX(ctx, key, owner, ttl).Result()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	lease := scheduler.NewBucketLease(func(releaseCtx context.Context) error {
		removed, err := releaseBucketOwner.Run(releaseCtx, s.client, []string{key}, owner).Int64()
		if err != nil {
			return err
		}
		if removed == 0 {
			return scheduler.ErrBucketLeaseLost
		}
		return nil
	})
	return lease, true, nil
}
