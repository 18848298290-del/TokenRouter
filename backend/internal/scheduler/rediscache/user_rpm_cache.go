package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// User and group RPM counters use Redis-clock minute buckets.
//
// Storage rules:
//   - Keys are rpm:ug:{uid}:{gid}:{minute} and rpm:u:{uid}:{minute}.
//   - Redis TIME supplies the shared clock for all instances.
//   - MULTI/EXEC groups INCR and EXPIRE for the same key.
//   - The 120-second TTL covers the current minute plus expiry margin.
//   - RPMAdmission.Check compares the returned count with the effective limit.
const (
	userGroupRPMKeyPrefix = "rpm:ug:"
	userRPMKeyPrefix      = "rpm:u:"

	userRPMKeyTTL = 120 * time.Second
)

type userRPMCacheImpl struct {
	rdb *redis.Client
}

// NewUserRPMCache creates Redis-backed user and group RPM counters.
func NewUserRPMCache(rdb *redis.Client) scheduler.UserRPMCache {
	return &userRPMCacheImpl{rdb: rdb}
}

// minuteTS returns Redis Unix seconds divided by 60.
func (c *userRPMCacheImpl) minuteTS(ctx context.Context) (int64, error) {
	t, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TIME: %w", err)
	}
	return t.Unix() / 60, nil
}

// atomicIncr increments and refreshes expiry in one Redis transaction.
func (c *userRPMCacheImpl) atomicIncr(ctx context.Context, key string) (int, error) {
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, userRPMKeyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("user rpm increment: %w", err)
	}
	return int(incr.Val()), nil
}

// IncrementUserGroupRPM increments the current (user, group) minute counter.
func (c *userRPMCacheImpl) IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
	return c.atomicIncr(ctx, key)
}

// IncrementUserRPM increments the user's current minute counter.
func (c *userRPMCacheImpl) IncrementUserRPM(ctx context.Context, userID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d", userRPMKeyPrefix, userID, minute)
	return c.atomicIncr(ctx, key)
}

// GetUserGroupRPM reads the current (user, group) count, returning zero for a missing key.
func (c *userRPMCacheImpl) GetUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d:%d", userGroupRPMKeyPrefix, userID, groupID, minute)
	val, err := c.rdb.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("user group rpm get: %w", err)
	}
	return val, nil
}

// GetUserRPM reads the user's current minute count, returning zero for a missing key.
func (c *userRPMCacheImpl) GetUserRPM(ctx context.Context, userID int64) (int, error) {
	minute, err := c.minuteTS(ctx)
	if err != nil {
		return 0, err
	}
	key := fmt.Sprintf("%s%d:%d", userRPMKeyPrefix, userID, minute)
	val, err := c.rdb.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("user rpm get: %w", err)
	}
	return val, nil
}
