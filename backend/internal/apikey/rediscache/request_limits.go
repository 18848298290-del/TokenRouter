package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

var (
	// reserveRequestScript uses Redis time for the 60-second RPM window and two-minute concurrency leases.
	// It writes only after both checks pass, so rejection does not consume the other limit.
	reserveRequestScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local concurrency = tonumber(ARGV[2])
local rpm = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now - 60000)
-- 同一次命令的重试返回已经预占的结果，RPM 时间戳保持首次放行时间。
local active = redis.call('ZSCORE', KEYS[1], ARGV[1])
local counted = redis.call('ZSCORE', KEYS[2], ARGV[1])
if (concurrency <= 0 or active) and (rpm <= 0 or counted) then
 return {0, 0}
end
if concurrency > 0 and not active and redis.call('ZCARD', KEYS[1]) >= concurrency then
 return {1, 1000}
end
if rpm > 0 and not counted and redis.call('ZCARD', KEYS[2]) >= rpm then
 local first = redis.call('ZRANGE', KEYS[2], 0, 0, 'WITHSCORES')
 return {2, math.max(1, tonumber(first[2]) + 60000 - now)}
end
if concurrency > 0 then
 redis.call('ZADD', KEYS[1], now + 120000, ARGV[1])
 redis.call('PEXPIRE', KEYS[1], 120000)
end
if rpm > 0 and not counted then
 redis.call('ZADD', KEYS[2], now, ARGV[1])
 redis.call('PEXPIRE', KEYS[2], 60000)
end
return {0, 0}
`)

	// refreshRequestScript renews unexpired leases; an expired lease requires the request to stop.
	refreshRequestScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local expires = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not expires or tonumber(expires) <= now then return 0 end
redis.call('ZADD', KEYS[1], now + 120000, ARGV[1])
redis.call('PEXPIRE', KEYS[1], 120000)
return 1
`)
)

// requestLimitKeys aggregates all groups and instances by key ID.
func requestLimitKeys(id int64) []string {
	prefix := fmt.Sprintf("apikey:requests:{%d}:", id)
	return []string{prefix + "active", prefix + "rpm"}
}

// ReserveRequest atomically checks concurrency and RPM and returns a retry delay on rejection.
func (c *ApiKeyCache) ReserveRequest(ctx context.Context, id int64, request string, concurrency, rpm int) (time.Duration, error) {
	result, err := reserveRequestScript.Run(ctx, c.rdb, requestLimitKeys(id), request, concurrency, rpm).Int64Slice()
	if err != nil || len(result) != 2 {
		return 0, apikey.ErrKeyLimiterUnavailable
	}
	retry := time.Duration(result[1]) * time.Millisecond
	switch result[0] {
	case 1:
		return retry, apikey.ErrKeyConcurrencyExceeded
	case 2:
		return retry, apikey.ErrKeyRPMExceeded
	default:
		return 0, nil
	}
}

// RefreshRequest extends an active request lease by two minutes.
func (c *ApiKeyCache) RefreshRequest(ctx context.Context, id int64, request string) error {
	result, err := refreshRequestScript.Run(ctx, c.rdb, requestLimitKeys(id)[:1], request).Int()
	if err != nil || result != 1 {
		return apikey.ErrKeyLimiterUnavailable
	}
	return nil
}

// ReleaseRequest returns the concurrency slot. RPM records remain in the 60-second window.
func (c *ApiKeyCache) ReleaseRequest(ctx context.Context, id int64, request string) error {
	return c.rdb.ZRem(ctx, requestLimitKeys(id)[0], request).Err()
}
