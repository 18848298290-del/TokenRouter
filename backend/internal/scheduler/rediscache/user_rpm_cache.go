package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// User-wide and user/group RPM use separate Redis-clock minute counters,
// allowing admission to compare each recorded count with its own limit. Key
// selection reads TIME before writing, and each INCR/EXPIRE transaction
// retains its counter for two minutes.

// User RPM prefixes distinguish total and group-scoped admission counts; the
// shared lifetime expires prior minute keys after their active counting
// interval.
const (
	userGroupRPMKeyPrefix = "rpm:ug:" // user and group counter namespace
	userRPMKeyPrefix      = "rpm:u:"  // total user counter namespace

	userRPMKeyTTL = 120 * time.Second // minute counter retention duration
)

// userRPMCacheImpl stores separate user and user/group Redis minute
// observations.
type userRPMCacheImpl struct {
	// rdb is supplied by the application and remains caller-owned while
	// admission reads and increments these shared counters.
	rdb *redis.Client // shared user counter client
}

// NewUserRPMCache constructs total and group-scoped user RPM storage over the
// supplied Redis client. The caller keeps that client usable for the
// admission operations using this cache.
func NewUserRPMCache(rdb *redis.Client) scheduler.UserRPMCache {
	return &userRPMCacheImpl{rdb: rdb}
}

// minuteTS returns the minute index sampled from Redis TIME for user counter
// key selection. A TIME failure aborts the caller before it can read or
// increment a selected key.
func (c *userRPMCacheImpl) minuteTS(ctx context.Context) (int64, error) {
	t, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TIME: %w", err)
	}
	// Convert shared Redis seconds into sixty-second counter indexes.
	return t.Unix() / 60, nil
}

// atomicIncr increments the supplied counter key and refreshes its two-minute
// expiry in one transaction. A failed response returns an error with an
// unconfirmed count; callers do not refund through this cache.
func (c *userRPMCacheImpl) atomicIncr(ctx context.Context, key string) (int, error) {
	// Queue INCR with its expiry before EXEC so every completed counting
	// transaction also refreshes retention for that already-resolved minute key.
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, userRPMKeyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("user rpm increment: %w", err)
	}
	return int(incr.Val()), nil
}

// IncrementUserGroupRPM records one request in the sampled minute for this
// user and group. It returns the incremented count for admission comparisons,
// with key selection preceding the write transaction.
func (c *userRPMCacheImpl) IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
	return c.atomicIncr(ctx, key)
}

// IncrementUserRPM records one request in this user's sampled minute across
// groups. It returns the incremented total for admission comparisons, with
// key selection preceding the write transaction.
func (c *userRPMCacheImpl) IncrementUserRPM(ctx context.Context, userID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d", userRPMKeyPrefix, userID, minute)
	return c.atomicIncr(ctx, key)
}

// GetUserGroupRPM reads the sampled minute count for one user/group pair. A
// missing key returns zero; Redis TIME, command and integer-conversion
// failures remain errors for admission callers.
func (c *userRPMCacheImpl) GetUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
	val, err := c.rdb.Get(ctx, key).Int()
	// An absent key represents a minute with no recorded request count, so
	// admission observations begin at zero for this sampled interval.
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("user group rpm get: %w", err)
	}
	return val, nil
}

// GetUserRPM reads this user's total count for the sampled Redis minute. A
// missing key returns zero; Redis TIME, command and integer-conversion
// failures remain errors for admission callers.
func (c *userRPMCacheImpl) GetUserRPM(ctx context.Context, userID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d", userRPMKeyPrefix, userID, minute)
	val, err := c.rdb.Get(ctx, key).Int()
	// An absent key represents a minute with no recorded request count, so
	// admission observations begin at zero for this sampled interval.
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("user rpm get: %w", err)
	}
	return val, nil
}
