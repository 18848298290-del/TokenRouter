package rediscache

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Provider lock and completion keys use providerID as a Redis Cluster hash tag to share a slot.
// Format: umq:{providerID}:lock and umq:{providerID}:last.
const (
	umqKeyPrefix  = "umq:"
	umqLockSuffix = ":lock" // STRING (requestID), PX lockTtlMs
	umqLastSuffix = ":last" // STRING containing a Unix millisecond timestamp, EX 60s.

	// The lock index supplies cleanup candidates without scanning every umq:*:lock key.
	// Members are provider IDs; scores are expected lock expiry times in Redis Unix milliseconds.
	umqLockIndexKey              = "umq:lock:index" // ZSET of provider IDs scored by lock expiry in Unix milliseconds.
	umqLockIndexCleanupBatchSize = 1000
)

var (
	// acquireLockScript acquires the serialization lock or renews it for the same request.
	// It returns acquisition status and observed expiry even when busy, so Go can repair the index.
	// A later contention attempt repairs missing entries from upgrades, failed index writes, or release races.
	// A lock with PTTL -1 is indexed at the current time for immediate cleanup.
	acquireLockScript = redis.NewScript(`
redis.replicate_commands()
local cur = redis.call('GET', KEYS[1])
local ttl = tonumber(ARGV[2])
if cur == ARGV[1] then
    redis.call('PEXPIRE', KEYS[1], ttl)
    local t = redis.call('TIME')
    local ms = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
    return {1, ms + ttl}
end
if cur ~= false then
    local t = redis.call('TIME')
    local ms = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
    local pttl = redis.call('PTTL', KEYS[1])
    if pttl and pttl > 0 then
        return {0, ms + pttl}
    end
    return {0, ms}
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
local t = redis.call('TIME')
local ms = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
return {1, ms + ttl}
`)

	// releaseLockScript releases the matching owner's lock and records completion using Redis TIME.
	releaseLockScript = redis.NewScript(`
-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
-- 5.0 及以上默认按效果复制；保留调用不改变行为。
redis.replicate_commands()
local cur = redis.call('GET', KEYS[1])
if cur == ARGV[1] then
    redis.call('DEL', KEYS[1])
    local t = redis.call('TIME')
    local ms = tonumber(t[1])*1000 + math.floor(tonumber(t[2])/1000)
    redis.call('SET', KEYS[2], ms, 'EX', 60)
    return 1
end
return 0
`)

	// reconcileLockScript checks the lock TTL and deletes locks with PTTL -1 in the same operation.
	// Returns {-2, 0} for no lock, {-1, 0} after deleting a persistent lock, or {1, remaining PTTL}.
	reconcileLockScript = redis.NewScript(`
local pttl = redis.call('PTTL', KEYS[1])
if pttl == -2 then
    return {-2, 0}
end
if pttl == -1 then
    redis.call('DEL', KEYS[1])
    return {-1, 0}
end
return {1, pttl}
`)
)

type userMsgQueueCache struct {
	rdb *redis.Client
}

// NewUserMsgQueueCache creates Redis storage for provider message serialization.
func NewUserMsgQueueCache(rdb *redis.Client) scheduler.UserMsgQueueCache {
	return &userMsgQueueCache{rdb: rdb}
}

func umqLockKey(providerID int64) string {
	// The braces in umq:{123}:lock make the provider ID a Redis Cluster hash tag.
	return umqKeyPrefix + "{" + strconv.FormatInt(providerID, 10) + "}" + umqLockSuffix
}

func umqLastKey(providerID int64) string {
	// umq:{123}:last shares the lock key's hash slot.
	return umqKeyPrefix + "{" + strconv.FormatInt(providerID, 10) + "}" + umqLastSuffix
}

// AcquireLock attempts to acquire the provider's serialization lock.
// After a successful script response, index either the acquired lock or the observed busy lock.
// Background reconciliation discovers contended locks through this index.
func (c *userMsgQueueCache) AcquireLock(ctx context.Context, providerID int64, requestID string, lockTtlMs int) (bool, error) {
	key := umqLockKey(providerID)
	result, err := acquireLockScript.Run(ctx, c.rdb, []string{key}, requestID, lockTtlMs).Result()
	if err != nil {
		return false, fmt.Errorf("umq acquire lock: %w", err)
	}
	acquired, err := redisScriptInt64At(result, 0)
	if err != nil {
		return false, fmt.Errorf("umq parse acquire lock result: %w", err)
	}
	expireAtMs, err := redisScriptInt64At(result, 1)
	if err != nil {
		return false, fmt.Errorf("umq parse acquire lock expire: %w", err)
	}
	if expireAtMs > 0 {
		if err := c.rdb.ZAdd(ctx, umqLockIndexKey, redis.Z{
			Score:  float64(expireAtMs),
			Member: strconv.FormatInt(providerID, 10),
		}).Err(); err != nil {
			logger.LegacyPrintf("repository.umq", "Warning: update lock index for provider %d failed: %v", providerID, err)
		}
	}
	return acquired == 1, nil
}

// ReleaseLock releases the matching request's lock and records completion.
// A successful release also removes the index entry; a different owner's lock is left intact.
func (c *userMsgQueueCache) ReleaseLock(ctx context.Context, providerID int64, requestID string) (bool, error) {
	lockKey := umqLockKey(providerID)
	lastKey := umqLastKey(providerID)
	result, err := releaseLockScript.Run(ctx, c.rdb, []string{lockKey, lastKey}, requestID).Int()
	if err != nil {
		return false, fmt.Errorf("umq release lock: %w", err)
	}
	if result == 1 {
		// This can race with the next owner's ZADD and remove that owner's new index entry.
		// The next contention attempt repairs the missing entry through AcquireLock.
		if err := c.rdb.ZRem(ctx, umqLockIndexKey, strconv.FormatInt(providerID, 10)).Err(); err != nil {
			logger.LegacyPrintf("repository.umq", "Warning: remove lock index for provider %d failed: %v", providerID, err)
		}
	}
	return result == 1, nil
}

// GetLastCompletedMs returns the last Redis completion timestamp in milliseconds, or zero when absent.
func (c *userMsgQueueCache) GetLastCompletedMs(ctx context.Context, providerID int64) (int64, error) {
	key := umqLastKey(providerID)
	val, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("umq get last completed: %w", err)
	}
	ms, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("umq parse last completed: %w", err)
	}
	return ms, nil
}

// GetCurrentTimeMs returns Redis Unix milliseconds using the same clock as lock completion.
func (c *userMsgQueueCache) GetCurrentTimeMs(ctx context.Context) (int64, error) {
	t, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("umq get redis time: %w", err)
	}
	return t.UnixMilli(), nil
}

// ReconcileExpiredLockCandidates processes index entries whose expected expiry has passed.
// It rechecks PTTL because a renewed lock can outlive a stale index score.
func (c *userMsgQueueCache) ReconcileExpiredLockCandidates(ctx context.Context, maxCount int) (int, error) {
	if maxCount <= 0 {
		maxCount = umqLockIndexCleanupBatchSize
	}
	nowMs, err := c.GetCurrentTimeMs(ctx)
	if err != nil {
		return 0, err
	}
	members, err := c.rdb.ZRangeByScore(ctx, umqLockIndexKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   strconv.FormatInt(nowMs, 10),
		Count: int64(maxCount),
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("umq read lock index: %w", err)
	}

	cleaned := 0
	for _, member := range members {
		providerID, err := strconv.ParseInt(member, 10, 64)
		if err != nil || providerID <= 0 {
			c.removeLockIndexMember(ctx, member)
			continue
		}

		result, err := reconcileLockScript.Run(ctx, c.rdb, []string{umqLockKey(providerID)}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return cleaned, fmt.Errorf("umq reconcile lock: %w", err)
		}
		status, err := redisScriptInt64At(result, 0)
		if err != nil {
			return cleaned, fmt.Errorf("umq parse reconcile status: %w", err)
		}
		pttl, err := redisScriptInt64At(result, 1)
		if err != nil {
			return cleaned, fmt.Errorf("umq parse reconcile pttl: %w", err)
		}

		switch status {
		case -2:
			// The lock has expired or been released; remove its stale index entry.
			c.removeLockIndexMember(ctx, member)
		case -1:
			// A persistent lock could block the queue indefinitely; the script has deleted it, so count this cleanup.
			c.removeLockIndexMember(ctx, member)
			cleaned++
		case 1:
			// The lock is still live; reschedule its index entry using the remaining PTTL.
			if err := c.rdb.ZAdd(ctx, umqLockIndexKey, redis.Z{
				Score:  float64(nowMs + pttl),
				Member: member,
			}).Err(); err != nil {
				logger.LegacyPrintf("repository.umq", "Warning: reschedule lock index member %s failed: %v", member, err)
			}
		}
	}
	return cleaned, nil
}

// removeLockIndexMember removes a stale index entry and logs maintenance errors.
func (c *userMsgQueueCache) removeLockIndexMember(ctx context.Context, member string) {
	if err := c.rdb.ZRem(ctx, umqLockIndexKey, member).Err(); err != nil {
		logger.LegacyPrintf("repository.umq", "Warning: remove lock index member %s failed: %v", member, err)
	}
}

// redisScriptInt64At decodes integer-like Lua array elements returned by go-redis.
func redisScriptInt64At(result any, index int) (int64, error) {
	values, ok := result.([]any)
	if !ok {
		return 0, fmt.Errorf("expected redis script array, got %T", result)
	}
	if index < 0 || index >= len(values) {
		return 0, fmt.Errorf("redis script array missing index %d", index)
	}
	switch v := values[index].(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected redis script value %T", v)
	}
}
