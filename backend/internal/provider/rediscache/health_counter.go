package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Provider health counters retain the fixed expiry established by the first
// failure, so repeated errors within that window accumulate without
// continually moving its end. Separate namespaces keep Antigravity INTERNAL
// 500, OpenAI 403, and timeout counts independent; each reset deletes the
// relevant provider key.
//
// One Lua operation increments and conditionally starts expiry before
// returning the shared count. The provider core decides whether that
// observation warrants a health change, while this adapter accepts the
// configured window and applies its minimum. Reads distinguish missing
// counters from Redis failures, and timeout lifetime queries expose the
// backend's nonpositive sentinels as well as its live countdown.

// These namespaces keep distinct provider failure classes separate; the fixed
// INTERNAL 500 lifetime and configured windows determine when each shared
// count expires.
const (
	internal500CounterPrefix     = "internal500_count:provider:"
	internal500CounterTTLSeconds = 86400
	openAI403CounterPrefix       = "openai_403_count:provider:"
	timeoutCounterPrefix         = "timeout_count:provider:"
)

// healthCounterIncrScript increments a shared failure count and establishes expiry
// only when the new count is one, retaining the first window for later observations.
var healthCounterIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, ttl)
	end

	return count
`)

// internal500CounterCache stores Antigravity INTERNAL 500 observations in
// shared Redis counters.
type internal500CounterCache struct {
	// rdb is borrowed from application assembly and must stay usable until health
	// operations finish; the application owns connection shutdown and
	// retry configuration.
	rdb *redis.Client
}

// openAI403CounterCache stores OpenAI 403 observations in shared Redis counters.
type openAI403CounterCache struct {
	// rdb is borrowed from application assembly and must stay usable until health
	// operations finish; the application owns connection shutdown and
	// retry configuration.
	rdb *redis.Client
}

// timeoutCounterCache stores provider timeout observations in shared Redis counters.
type timeoutCounterCache struct {
	// rdb is borrowed from application assembly and must stay usable until health
	// operations finish; the application owns connection shutdown and
	// retry configuration.
	rdb *redis.Client
}

// NewInternal500CounterCache retains the supplied Redis client for
// Antigravity INTERNAL 500 counts. The caller owns client lifetime and
// resets; counting uses a fixed twenty-four-hour window established by the
// first increment.
func NewInternal500CounterCache(rdb *redis.Client) provider.Internal500CounterCache {
	return &internal500CounterCache{rdb: rdb}
}

// IncrementInternal500Count returns the incremented provider count in the
// fixed twenty-four-hour window. The Lua operation sets expiry on count one
// and retains it later; Redis errors return a wrapped failure for the core's
// health decision.
func (c *internal500CounterCache) IncrementInternal500Count(ctx context.Context, providerID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", internal500CounterPrefix, providerID)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, internal500CounterTTLSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment internal500 count: %w", err)
	}

	return result, nil
}

// ResetInternal500Count deletes the provider's INTERNAL 500 counter after
// core recovery decisions. An absent key succeeds, and a Redis failure is
// returned so the caller can observe incomplete shared-counter cleanup.
func (c *internal500CounterCache) ResetInternal500Count(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", internal500CounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// NewOpenAI403CounterCache retains the supplied Redis client for OpenAI 403
// observations. The caller owns the connection and threshold policy; each
// first increment establishes the duration supplied to that increment.
func NewOpenAI403CounterCache(rdb *redis.Client) provider.OpenAI403CounterCache {
	return &openAI403CounterCache{rdb: rdb}
}

// IncrementOpenAI403Count records a provider failure and returns its
// fixed-window count. The first increment uses windowMinutes with a
// one-minute minimum; later increments retain the existing expiry even when
// another duration is supplied.
func (c *openAI403CounterCache) IncrementOpenAI403Count(ctx context.Context, providerID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", openAI403CounterPrefix, providerID)

	// Convert minutes to Redis seconds with a one-minute floor.
	ttlSeconds := max(windowMinutes*60, 60)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment openai 403 count: %w", err)
	}
	return result, nil
}

// ResetOpenAI403Count removes the provider's shared 403 observation counter
// when requested by the core. Missing keys succeed, and Redis deletion
// failures remain available to the caller.
func (c *openAI403CounterCache) ResetOpenAI403Count(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", openAI403CounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// NewTimeoutCounterCache retains a caller-owned Redis client for provider
// timeout observations. The core supplies each window and decides recovery or
// thresholds; keep the client open until those cache operations finish.
func NewTimeoutCounterCache(rdb *redis.Client) provider.TimeoutCounterCache {
	return &timeoutCounterCache{rdb: rdb}
}

// IncrementTimeoutCount increments the provider's count and returns the
// shared fixed-window observation. The first increment clamps windowMinutes
// to at least one minute, while subsequent increments keep the existing
// expiry regardless of the supplied duration.
func (c *timeoutCounterCache) IncrementTimeoutCount(ctx context.Context, providerID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)

	ttlSeconds := max(windowMinutes*60,
		// Keep expiry positive when a timeout policy supplies no window.
		60)

	result, err := healthCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment timeout count: %w", err)
	}

	return result, nil
}

// GetTimeoutCount returns the provider's stored count or zero for an absent
// key. Other Redis or integer-decoding errors are wrapped, allowing the
// caller to distinguish empty history from an unavailable or malformed
// counter.
func (c *timeoutCounterCache) GetTimeoutCount(ctx context.Context, providerID int64) (int64, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)

	val, err := c.rdb.Get(ctx, key).Int64()
	// Expired or reset counters represent no observed failures, allowing core health
	// decisions to use zero while other Redis failures retain their diagnostic error.
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get timeout count: %w", err)
	}

	return val, nil
}

// ResetTimeoutCount deletes the provider's shared timeout counter and permits
// repeated cleanup. A missing key succeeds; Redis deletion errors are
// returned to the core performing the recovery action.
func (c *timeoutCounterCache) ResetTimeoutCount(ctx context.Context, providerID int64) error {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)
	return c.rdb.Del(ctx, key).Err()
}

// GetTimeoutCountTTL returns the counter's remaining Redis lifetime using
// whole-second precision. Pinned go-redis retains missing-key and no-expiry
// sentinels as -2ns and -1ns respectively; callers interpret nonpositive
// values separately from a live countdown.
func (c *timeoutCounterCache) GetTimeoutCountTTL(ctx context.Context, providerID int64) (time.Duration, error) {
	key := fmt.Sprintf("%s%d", timeoutCounterPrefix, providerID)
	return c.rdb.TTL(ctx, key).Result()
}
