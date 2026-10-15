package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// request_limits.go reserves, renews, and releases API key request capacity
// with Redis scripts.
// Request admission uses two Redis sorted sets per Key ID: active leases expire
// after two minutes, while RPM members retain their first admission time for
// sixty seconds. Both sets share a hash tag so one Lua operation can prune
// ended records, check configured limits and publish a reservation atomically
// for all groups and instances using that Key.
//
// A replay reuses existing request members while their required records remain
// present; rejection adds neither member, and releasing a request removes its
// active member alone. Renewal requires an unexpired lease and extends it using
// Redis server time. Lost, undecodable or wrong-length reservation replies become
// limiter-unavailable errors so gateway callers reject uncertain new admission
// and perform independent cleanup.

// Redis scripts coordinate admission and renewal with server time so
// independent instances share the same active and rolling request counts.
var (
	// reserveRequestScript uses Redis time to prune ended lease and RPM members
	// before checking both allowances. Existing request identities support command
	// replay, and a rejection publishes neither new active nor RPM member.
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

	// refreshRequestScript checks the stored lease before extending its server-time
	// deadline. Missing or elapsed members cannot be resurrected by renewal, so
	// callers stop processing when the script returns its failure result.
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

// requestLimitKeys returns the active name followed by the RPM name in one
// Key-ID namespace. The slice is newly allocated; callers retain that order for
// Lua script arguments or selection of the active set.
func requestLimitKeys(id int64) []string {
	// Keep the Key ID inside braces so both sorted sets share a Redis Cluster slot
	// for the reservation script.
	prefix := fmt.Sprintf("apikey:requests:{%d}:", id)
	return []string{prefix + "active", prefix + "rpm"}
}

// ReserveRequest atomically reserves configured active and RPM allowances for
// one request identity, returning a retry delay on rejection. Nonpositive
// limits disable their respective checks, and records for other groups share
// the supplied Key ID.
//
// The script prunes expired members before checking replay and capacity.
// Existing required members let a replay succeed without moving its RPM
// timestamp; rejected requests add no member. A full active set returns a
// one-second retry, while RPM rejection uses its oldest admission expiry.
// Script failures or malformed result lengths return limiter unavailable;
// callers handle an uncertain reservation outcome.
func (c *ApiKeyCache) ReserveRequest(ctx context.Context, id int64, request string, concurrency, rpm int) (time.Duration, error) {
	result, err := reserveRequestScript.Run(ctx, c.rdb, requestLimitKeys(id), request, concurrency, rpm).Int64Slice()
	// Classify failed transport or malformed replies as unavailable because this
	// adapter cannot establish whether Redis applied a reservation before the
	// response failed.
	if err != nil || len(result) != 2 {
		return 0, apikey.ErrKeyLimiterUnavailable
	}
	// Lua retry values are milliseconds for the gateway's Retry-After conversion.
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

// RefreshRequest extends an unexpired active request lease by two minutes using
// Redis server time. Missing, expired or failed renewal returns
// ErrKeyLimiterUnavailable, telling the service to abort processing that has
// lost its reservation.
func (c *ApiKeyCache) RefreshRequest(ctx context.Context, id int64, request string) error {
	result, err := refreshRequestScript.Run(ctx, c.rdb, requestLimitKeys(id)[:1], request).Int()
	// A failed or non-successful acknowledgement leaves renewal unconfirmed, so
	// the service receives the unavailable identity and stops work relying on this
	// lease.
	if err != nil || result != 1 {
		return apikey.ErrKeyLimiterUnavailable
	}
	return nil
}

// ReleaseRequest removes the request's active member while retaining its RPM
// admission record. Removing an absent member succeeds according to Redis ZREM
// behavior; returned transport errors leave cleanup outcome uncertain for the
// caller.
func (c *ApiKeyCache) ReleaseRequest(ctx context.Context, id int64, request string) error {
	return c.rdb.ZRem(ctx, requestLimitKeys(id)[0], request).Err()
}
