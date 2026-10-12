package redis

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var rateLimitScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
local repaired = 0
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
elseif ttl == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  repaired = 1
end
return {current, repaired}
`)

// FixedWindowLimiter atomically counts requests in Redis under a caller-supplied prefix.
type FixedWindowLimiter struct {
	redis  *redis.Client
	prefix string
}

type allowResult struct {
	// Allowed reports whether the count is within the limit.
	Allowed bool
	// Count includes this request.
	Count int64
	// RetryAfter is the remaining window when blocked; unavailable PTTL uses the full window.
	RetryAfter time.Duration
}

// NewFixedWindowLimiter creates a counter; the caller owns the Redis client.
func NewFixedWindowLimiter(client *redis.Client, prefix string) *FixedWindowLimiter {
	return &FixedWindowLimiter{redis: client, prefix: prefix}
}

func runFixedWindowScript(ctx context.Context, client *redis.Client, key string, windowMillis int64) (int64, bool, error) {
	values, err := rateLimitScript.Run(ctx, client, []string{key}, windowMillis).Slice()
	if err != nil {
		return 0, false, err
	}
	if len(values) < 2 {
		return 0, false, fmt.Errorf("rate limit script returned %d values", len(values))
	}
	count, err := parseInt64(values[0])
	if err != nil {
		return 0, false, err
	}
	repaired, err := parseInt64(values[1])
	if err != nil {
		return 0, false, err
	}
	return count, repaired == 1, nil
}

// Allow increments the prefixed key and returns admission, count, and retry delay.
// Pass key without the configured prefix. Callers decide whether Redis errors
// should allow or reject requests.
// @project-doc docs/operations/edge_security.md#fixed_window_rate_limits
func (r *FixedWindowLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int64, time.Duration, error) {
	redisKey := r.prefix + key
	windowMillis := windowTTLMillis(window)

	count, repaired, err := runFixedWindowScript(ctx, r.redis, redisKey, windowMillis)
	if err != nil {
		return false, 0, 0, err
	}
	if repaired {
		log.Printf("[RateLimit] ttl repaired: key=%s window_ms=%d", redisKey, windowMillis)
	}

	result := allowResult{Allowed: count <= int64(limit), Count: count}
	if !result.Allowed {
		result.RetryAfter = window
		if ttl, ttlErr := r.redis.PTTL(ctx, redisKey).Result(); ttlErr == nil && ttl > 0 {
			result.RetryAfter = ttl
		}
	}
	return result.Allowed, result.Count, result.RetryAfter, nil
}

func windowTTLMillis(window time.Duration) int64 {
	ttl := window.Milliseconds()
	if ttl < 1 {
		return 1
	}
	return ttl
}

func parseInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unexpected value type %T", value)
	}
}
