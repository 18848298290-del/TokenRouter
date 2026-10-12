package rediscache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Provider RPM uses a counter for each Redis-clock minute.
//
// Counter format:
// Each provider has an independent minute bucket.
// - Key: rpm:{providerID}:{minuteTimestamp}
// Value: requests recorded in that minute.
// TTL: 120 seconds, covering the minute plus expiry margin.
//
// MULTI/EXEC groups INCR and EXPIRE for the same key.
// Redis TIME supplies a shared clock across instances.
//
// The minute determines the key before the write transaction starts.
// TIME therefore runs separately from MULTI/EXEC.
// The transaction needs the resolved key; it cannot use an earlier pipeline result.
// Single-key writes also avoid Lua scripts that construct keys dynamically.
const (
	// Provider RPM key prefix.
	// Format: rpm:{providerID}:{minuteTimestamp}.
	rpmKeyPrefix = "rpm:"

	// Keep counters for two minutes so the current bucket remains available.
	rpmKeyTTL = 120 * time.Second
)

// RPMCacheImpl stores provider RPM counters in Redis.
type RPMCacheImpl struct {
	rdb *redis.Client
}

// NewRPMCache creates Redis-backed provider RPM storage.
func NewRPMCache(rdb *redis.Client) scheduler.RPMCache {
	return &RPMCacheImpl{rdb: rdb}
}

// currentMinuteKey returns the provider key for the current Redis-clock minute.
// The suffix is Unix seconds divided by 60.
func (c *RPMCacheImpl) currentMinuteKey(ctx context.Context, providerID int64) (string, error) {
	serverTime, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("redis TIME: %w", err)
	}
	minuteTS := serverTime.Unix() / 60
	return fmt.Sprintf("%s%d:%d", rpmKeyPrefix, providerID, minuteTS), nil
}

// currentMinuteSuffix returns the Redis-clock minute suffix for batch reads.
// All keys in a batch use this one time sample.
func (c *RPMCacheImpl) currentMinuteSuffix(ctx context.Context) (string, error) {
	serverTime, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("redis TIME: %w", err)
	}
	minuteTS := serverTime.Unix() / 60
	return strconv.FormatInt(minuteTS, 10), nil
}

// IncrementRPM increments and returns the current minute's count.
// INCR and EXPIRE execute together in MULTI/EXEC on the same key.
func (c *RPMCacheImpl) IncrementRPM(ctx context.Context, providerID int64) (int, error) {
	key, err := c.currentMinuteKey(ctx, providerID)
	if err != nil {
		return 0, fmt.Errorf("rpm increment: %w", err)
	}

	// Group the counter increment and expiry update in one transaction.
	// Each increment refreshes the counter's lifetime.
	pipe := c.rdb.TxPipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, rpmKeyTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("rpm increment: %w", err)
	}

	return int(incrCmd.Val()), nil
}

// GetRPM returns the current minute's count, or zero when the key is absent.
func (c *RPMCacheImpl) GetRPM(ctx context.Context, providerID int64) (int, error) {
	key, err := c.currentMinuteKey(ctx, providerID)
	if err != nil {
		return 0, fmt.Errorf("rpm get: %w", err)
	}

	val, err := c.rdb.Get(ctx, key).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil // No requests recorded in this minute.
	}
	if err != nil {
		return 0, fmt.Errorf("rpm get: %w", err)
	}
	return val, nil
}

// GetRPMBatch pipelines reads for multiple providers in the same minute.
func (c *RPMCacheImpl) GetRPMBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	if len(providerIDs) == 0 {
		return map[int64]int{}, nil
	}

	// Sample the minute once for the whole batch.
	minuteSuffix, err := c.currentMinuteSuffix(ctx)
	if err != nil {
		return nil, fmt.Errorf("rpm batch get: %w", err)
	}

	pipe := c.rdb.Pipeline()
	cmds := make(map[int64]*redis.StringCmd, len(providerIDs))
	for _, id := range providerIDs {
		key := fmt.Sprintf("%s%d:%s", rpmKeyPrefix, id, minuteSuffix)
		cmds[id] = pipe.Get(ctx, key)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("rpm batch get: %w", err)
	}

	result := make(map[int64]int, len(providerIDs))
	for id, cmd := range cmds {
		if val, err := cmd.Int(); err == nil {
			result[id] = val
		} else {
			result[id] = 0
		}
	}
	return result, nil
}
