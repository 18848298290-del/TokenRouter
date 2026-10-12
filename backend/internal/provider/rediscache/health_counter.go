package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

const (
	internal500CounterPrefix     = "internal500_count:provider:"
	internal500CounterTTLSeconds = 86400
	openAI403CounterPrefix       = "openai_403_count:provider:"
	timeoutCounterPrefix         = "timeout_count:provider:"
)

// healthCounterIncrScript starts a fixed window on the first count; later increments keep its expiry.
var healthCounterIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, ttl)
	end

	return count
`)

type internal500CounterCache struct {
	rdb *redis.Client
}

type openAI403CounterCache struct {
	rdb *redis.Client
}

type timeoutCounterCache struct {
	rdb *redis.Client
}

// NewInternal500CounterCache creates a cache for consecutive INTERNAL 500 failures.
func NewInternal500CounterCache(rdb *redis.Client) provider.Internal500CounterCache {
	return &internal500CounterCache{rdb: rdb}
}

// IncrementInternal500Count atomically increments and returns the count.
func (c *internal500CounterCache) IncrementInternal500Count(ctx context.Context, providerID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", internal500CounterPrefix, providerID)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, internal500CounterTTLSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment internal500 count: %w", err)
	}

	return result, nil
}

// ResetInternal500Count deletes the failure counter after recovery.
func (c *internal500CounterCache) ResetInternal500Count(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", internal500CounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

func NewOpenAI403CounterCache(rdb *redis.Client) provider.OpenAI403CounterCache {
	return &openAI403CounterCache{rdb: rdb}
}

func (c *openAI403CounterCache) IncrementOpenAI403Count(ctx context.Context, providerID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", openAI403CounterPrefix, providerID)

	ttlSeconds := max(windowMinutes*60, 60)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment openai 403 count: %w", err)
	}
	return result, nil
}

func (c *openAI403CounterCache) ResetOpenAI403Count(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", openAI403CounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// NewTimeoutCounterCache creates a provider timeout counter cache.
func NewTimeoutCounterCache(rdb *redis.Client) provider.TimeoutCounterCache {
	return &timeoutCounterCache{rdb: rdb}
}

// IncrementTimeoutCount increments and returns the provider's timeout count.
// windowMinutes sets the fixed window on the first increment, with a one-minute minimum.
func (c *timeoutCounterCache) IncrementTimeoutCount(ctx context.Context, providerID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)

	ttlSeconds := max(windowMinutes*60,
		// one-minute minimum
		60)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment timeout count: %w", err)
	}

	return result, nil
}

// GetTimeoutCount returns the provider's timeout count, or zero when absent.
func (c *timeoutCounterCache) GetTimeoutCount(ctx context.Context, providerID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)

	val, err := c.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get timeout count: %w", err)
	}

	return val, nil
}

// ResetTimeoutCount deletes the provider's timeout counter.
func (c *timeoutCounterCache) ResetTimeoutCount(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// GetTimeoutCountTTL returns Redis's remaining lifetime for the counter.
func (c *timeoutCounterCache) GetTimeoutCountTTL(ctx context.Context, providerID int64) (time.Duration, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)
	return c.rdb.TTL(ctx, key).Result()
}
