package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// redeem.go stores failed redemption counts and temporary code locks in Redis.
// Redemption failures are counted by user, while a separate key excludes
// concurrent attempts for one exact code string. Each failed-attempt increment
// also renews its twenty-four-hour expiry through a pipeline, so the counter
// lifetime follows the latest successful expiry command.
//
// The code lock stores a fixed value and has no owner token. Acquire uses
// SETNX with the supplied duration, while release deletes the current key
// unconditionally. Callers coordinate lease lifetime and release ordering; the
// redemption service still performs its authoritative state checks and
// entitlement writes in the database transaction.

const (
	// Redemption key prefixes keep user failure counts separate from exact-code
	// advisory locks, allowing each identity to use its independent expiry and
	// release rules.
	redeemRateLimitKeyPrefix = "redeem:ratelimit:"
	redeemLockKeyPrefix      = "redeem:lock:"
	// redeemRateLimitDuration sets the failed-attempt counter lifetime to
	// twenty-four hours, renewed by the expiry command after each recorded
	// increment attempt.
	redeemRateLimitDuration = 24 * time.Hour
)

// RedeemCache stores user failure counters and advisory code locks through a
// shared Redis client.
type RedeemCache struct {
	// rdb is shared with the caller and supplies counters/locks directly. The
	// application keeps it open until redemption cache operations have stopped.
	rdb *redis.Client // shared redemption Redis client
}

// redeemRateLimitKey returns the attempt namespace plus the supplied decimal
// user ID.
func redeemRateLimitKey(userID int64) string {
	return fmt.Sprintf("%s%d", redeemRateLimitKeyPrefix, userID)
}

// redeemLockKey returns the lock namespace plus the exact supplied code text.
func redeemLockKey(code string) string {
	return redeemLockKeyPrefix + code
}

// NewRedeemCache binds a caller-owned Redis client without creating counters
// or locks. Methods require a usable client/context; the caller owns client
// closure and orders it after redemption consumers stop.
func NewRedeemCache(rdb *redis.Client) *RedeemCache {
	return &RedeemCache{rdb: rdb}
}

// GetRedeemAttemptCount returns the stored integer counter or zero for
// redis.Nil. Other Redis/decoding errors return with their companion count, so
// only a missing key is treated as an absent failure history.
func (c *RedeemCache) GetRedeemAttemptCount(ctx context.Context, userID int64) (int, error) {
	key := redeemRateLimitKey(userID)
	count, err := c.rdb.Get(ctx, key).Int()
	if err == redis.Nil {
		// A missing counter means no saved failed attempts for this user, so the
		// redemption admission check receives zero without a cache error.
		return 0, nil
	}
	return count, err
}

// IncrementRedeemAttemptCount increments the user counter and renews its
// twenty-four-hour expiry through a pipeline. INCR and EXPIRE are separate
// commands, so errors can leave partial effects and callers cannot infer
// whether the counter advanced from the error alone.
func (c *RedeemCache) IncrementRedeemAttemptCount(ctx context.Context, userID int64) error {
	key := redeemRateLimitKey(userID)
	// Increment precedes the expiry command in this pipeline; partial or retried
	// execution can affect the saved counter before the caller sees an error.
	pipe := c.rdb.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, redeemRateLimitDuration)
	_, err := pipe.Exec(ctx)
	return err
}

// AcquireRedeemLock sets a fixed-value code key when absent and reports
// acquisition. False with no error means a key already exists; zero ttl stores
// a persistent key, so callers select positive expiry when needed. The lock
// carries no owner token for release.
func (c *RedeemCache) AcquireRedeemLock(ctx context.Context, code string, ttl time.Duration) (bool, error) {
	key := redeemLockKey(code)
	return c.rdb.SetNX(ctx, key, 1, ttl).Result()
}

// ReleaseRedeemLock unconditionally deletes the current key for the exact
// code. Missing keys succeed, while Redis errors return; callers coordinate
// lifetime and release because this operation cannot distinguish an expired
// lease's replacement holder.
func (c *RedeemCache) ReleaseRedeemLock(ctx context.Context, code string) error {
	key := redeemLockKey(code)
	// Delete the current code key directly; no owner identity is stored to
	// protect a different acquisition that may follow lease expiry.
	return c.rdb.Del(ctx, key).Err()
}
