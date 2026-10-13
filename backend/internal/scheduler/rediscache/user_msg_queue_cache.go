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

// Provider message serialization uses an owner-valued lock and a recent
// completion timestamp under the same provider hash tag. The lock script
// admits a free key or renews the same request, and release writes Redis's
// completion time when the stored owner matches the releasing request.
//
// A separate sorted-set index lets cleanup find due locks without scanning
// every lock key. Index writes happen after lock scripts, so a release can
// remove a successor's index entry and index errors can leave a lock
// undiscoverable. Later contention records the observed expiry to repair that
// discovery state. Reconciliation rechecks PTTL, deletes persistent locks and
// reschedules live locks whose index score is stale.

// Message queue keys place each provider's lock and completion value in one
// hash slot; the global index records expiry candidates for bounded
// maintenance.
const (
	umqKeyPrefix  = "umq:"  // provider serialization key namespace
	umqLockSuffix = ":lock" // request owner string suffix
	umqLastSuffix = ":last" // completion millisecond string suffix

	// umqLockIndexKey stores provider IDs scored by observed Redis expiry
	// milliseconds; cleanup rechecks the lock because index updates are separate
	// from ownership writes.
	umqLockIndexKey              = "umq:lock:index" // provider expiry candidate sorted set
	umqLockIndexCleanupBatchSize = 1000             // default reconciliation candidate count
)

// Serialization scripts resolve lock ownership and completion writes within
// Redis; reconciliation repairs expiry anomalies before Go updates the
// separate discovery index.
var (
	// acquireLockScript acquires a free lock or renews the same request owner
	// and returns status with observed expiry milliseconds. Busy locks also
	// return expiry so Go can repair their index entries; persistent locks are
	// indexed as due immediately.
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

	// releaseLockScript deletes a matching owner and stores its Redis completion
	// milliseconds with a sixty-second lifetime. A different or missing owner
	// returns zero and leaves the completion value unchanged.
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

	// reconcileLockScript rechecks PTTL and deletes a persistent lock within one
	// Redis call. Missing, persistent and live locks return distinct
	// status/remaining-TTL pairs for index cleanup.
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

// userMsgQueueCache stores provider serialization ownership, completion times
// and reconciliation candidates.
type userMsgQueueCache struct {
	// rdb remains caller-owned while lock holders may release or maintenance may
	// reconcile index candidates through this shared client.
	rdb *redis.Client // shared serialization storage client
}

// NewUserMsgQueueCache constructs provider serialization storage over the
// supplied Redis client. The caller retains that client through lock release
// and background reconciliation of provider expiry candidates.
func NewUserMsgQueueCache(rdb *redis.Client) scheduler.UserMsgQueueCache {
	return &userMsgQueueCache{rdb: rdb}
}

// umqLockKey returns the owner-lock key with the provider's shared hash tag.
func umqLockKey(providerID int64) string {
	return umqKeyPrefix + "{" + strconv.FormatInt(providerID, 10) + "}" + umqLockSuffix
}

// umqLastKey returns the completion-time key using the lock's provider hash
// tag.
func umqLastKey(providerID int64) string {
	return umqKeyPrefix + "{" + strconv.FormatInt(providerID, 10) + "}" + umqLastSuffix
}

// AcquireLock attempts the provider lock or renews it when requestID is
// already the owner. Callers supply a positive millisecond lifetime and
// release successful ownership with the same request identity; a nonpositive
// lifetime can delete a matching owner or make new-lock creation fail.
//
// The script returns the observed expiry for both acquired and busy locks.
// After parsing both values, Go writes an index candidate so cleanup can find
// an expired or persistent lock even after an earlier index loss. Index write
// failures are logged and acquisition status still returns successfully; lock
// mutation can already have happened before a response or parse error.
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
	// Record the observed lock as a cleanup candidate even on contention; losing
	// an index write leaves ownership intact and later contenders can repair
	// discovery.
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

// ReleaseLock releases a matching request owner and records its completion
// time using Redis's clock. Missing or different owners return false; a
// successful script writes the recent completion key before Go removes the
// index candidate.
//
// That index removal is a separate command and can race with a successor's
// acquisition/index write. The successor lock remains held, but its discovery
// entry can disappear until later contention repairs it. Index removal
// failures are logged while the successful ownership release returns true.
func (c *userMsgQueueCache) ReleaseLock(ctx context.Context, providerID int64, requestID string) (bool, error) {
	lockKey := umqLockKey(providerID)
	lastKey := umqLastKey(providerID)
	result, err := releaseLockScript.Run(ctx, c.rdb, []string{lockKey, lastKey}, requestID).Int()
	if err != nil {
		return false, fmt.Errorf("umq release lock: %w", err)
	}
	if result == 1 {
		// Remove this provider's discovery member after ownership release; a
		// successor may have indexed meanwhile, so later contention also repairs
		// missing candidates.
		if err := c.rdb.ZRem(ctx, umqLockIndexKey, strconv.FormatInt(providerID, 10)).Err(); err != nil {
			logger.LegacyPrintf("repository.umq", "Warning: remove lock index for provider %d failed: %v", providerID, err)
		}
	}
	return result == 1, nil
}

// GetLastCompletedMs reads this provider's completion time as Redis Unix
// milliseconds. A missing or expired completion key returns zero; malformed
// stored integers and Redis command errors remain failures.
func (c *userMsgQueueCache) GetLastCompletedMs(ctx context.Context, providerID int64) (int64, error) {
	key := umqLastKey(providerID)
	val, err := c.rdb.Get(ctx, key).Result()
	// An expired or absent completion key supplies no recent spacing
	// observation, so scheduler message timing receives zero and can apply its
	// admission policy.
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

// GetCurrentTimeMs returns Redis Unix milliseconds for comparison with
// serialization completion times.
func (c *userMsgQueueCache) GetCurrentTimeMs(ctx context.Context) (int64, error) {
	t, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("umq get redis time: %w", err)
	}
	// Use millisecond resolution to match the completion script's timestamps.
	return t.UnixMilli(), nil
}

// ReconcileExpiredLockCandidates checks at most maxCount due index members
// and returns the number of persistent locks deleted. Nonpositive counts
// select the default batch, and an error may follow earlier completed
// cleanups.
//
// Each candidate's current PTTL decides the action: absent locks lose their
// stale index member, persistent locks are deleted, and live locks receive a
// revised score. The returned count excludes naturally expired keys and
// malformed IDs. Index repair failures are logged and processing continues,
// while lock-script or decoding failures stop the batch with its completed
// count.
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
		// Malformed or nonpositive provider identities cannot locate a usable lock
		// key; remove their discovery entries and continue checking valid
		// candidates.
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
			// Redis reports no current lock, so this due index entry is stale;
			// removing it does not count a persistent-lock deletion.
			c.removeLockIndexMember(ctx, member)
		case -1:
			// The script deleted a lock with no expiry, freeing indefinite
			// serialization blockage; count that deletion even when later index
			// removal needs another attempt.
			c.removeLockIndexMember(ctx, member)
			cleaned++
		case 1:
			// The lock still has a lifetime, so its due score was stale; reschedule
			// from the sampled Redis time and rechecked remaining PTTL.
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

// removeLockIndexMember attempts to remove a stale discovery member and logs
// Redis failures while the current caller continues. A failure leaves removal
// unconfirmed; a candidate that remains stored is available to a later pass.
func (c *userMsgQueueCache) removeLockIndexMember(ctx context.Context, member string) {
	// Continue reconciliation after an index error because the primary lock
	// decision is already complete. A lost response can follow successful
	// removal, so logging reports an unconfirmed index result.
	if err := c.rdb.ZRem(ctx, umqLockIndexKey, member).Err(); err != nil {
		logger.LegacyPrintf("repository.umq", "Warning: remove lock index member %s failed: %v", member, err)
	}
}

// redisScriptInt64At decodes one integer-like element from a Lua array
// response. It accepts int64, int, decimal strings and decimal byte slices;
// unsupported arrays, missing indexes and conversion failures return errors.
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
