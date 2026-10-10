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

// healthCounterIncrScript 在首次计数时设置固定窗口，后续递增保持到期时间。
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

// NewInternal500CounterCache 创建 INTERNAL 500 连续失败计数器缓存实例。
func NewInternal500CounterCache(rdb *redis.Client) provider.Internal500CounterCache {
	return &internal500CounterCache{rdb: rdb}
}

// IncrementInternal500Count 原子递增计数并返回当前值。
func (c *internal500CounterCache) IncrementInternal500Count(ctx context.Context, providerID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", internal500CounterPrefix, providerID)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, internal500CounterTTLSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment internal500 count: %w", err)
	}

	return result, nil
}

// ResetInternal500Count 清零计数器（成功响应时调用）。
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

// NewTimeoutCounterCache 创建超时计数器缓存实例。
func NewTimeoutCounterCache(rdb *redis.Client) provider.TimeoutCounterCache {
	return &timeoutCounterCache{rdb: rdb}
}

// IncrementTimeoutCount 增加提供商的超时计数，返回当前计数值
// windowMinutes 是计数窗口时间（分钟），超过此时间计数器会自动重置。
func (c *timeoutCounterCache) IncrementTimeoutCount(ctx context.Context, providerID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)

	ttlSeconds := max(windowMinutes*60,
		// 最小1分钟
		60)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment timeout count: %w", err)
	}

	return result, nil
}

// GetTimeoutCount 获取提供商当前的超时计数。
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

// ResetTimeoutCount 重置提供商的超时计数。
func (c *timeoutCounterCache) ResetTimeoutCount(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// GetTimeoutCountTTL 获取计数器剩余过期时间。
func (c *timeoutCounterCache) GetTimeoutCountTTL(ctx context.Context, providerID int64) (time.Duration, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)
	return c.rdb.TTL(ctx, key).Result()
}
