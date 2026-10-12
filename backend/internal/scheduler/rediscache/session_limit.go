package rediscache

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Active sessions use a sorted set for each provider.
//
// Session representation:
// The set tracks session activity used for max_sessions admission.
// - Key: session_limit:provider:{providerID}
// Member: sessionUUID extracted from metadata.user_id.
// Score: last activity time in Unix seconds.
//
// Score-based pruning removes idle sessions; key TTL also removes inactive sets.
const (
	// Provider session key prefix.
	// Format: session_limit:provider:{providerID}.
	sessionLimitKeyPrefix = "session_limit:provider:"
)

var (
	// registerSessionScript refreshes an existing session or admits a new one below maxSessions.
	// Redis TIME supplies the clock shared by instances.
	// KEYS[1] = session_limit:provider:{providerID}
	// ARGV[1] = maxSessions
	// ARGV[2] = idleTimeout in seconds.
	// ARGV[3] = sessionUUID
	// Returns 1 for admission, 0 for rejection.
	registerSessionScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local maxSessions = tonumber(ARGV[1])
		local idleTimeout = tonumber(ARGV[2])
		local sessionUUID = ARGV[3]

		-- 使用 Redis 服务器时间，确保多实例时钟一致
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - idleTimeout

		-- 清理过期会话
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)

		-- 检查会话是否已存在（支持刷新时间戳）
		local exists = redis.call('ZSCORE', key, sessionUUID)
		if exists ~= false then
			-- 会话已存在，刷新时间戳
			redis.call('ZADD', key, now, sessionUUID)
			redis.call('EXPIRE', key, idleTimeout + 60)
			return 1
		end

		-- 检查是否达到会话数量上限
		local count = redis.call('ZCARD', key)
		if count < maxSessions then
			-- 未达上限，添加新会话
			redis.call('ZADD', key, now, sessionUUID)
			redis.call('EXPIRE', key, idleTimeout + 60)
			return 1
		end

		-- 达到上限，拒绝新会话
		return 0
	`)

	// refreshSessionScript refreshes the timestamp of an existing session.
	// KEYS[1] = session_limit:provider:{providerID}
	// ARGV[1] = idleTimeout in seconds.
	// ARGV[2] = sessionUUID.
	refreshSessionScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local idleTimeout = tonumber(ARGV[1])
		local sessionUUID = ARGV[2]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])

		-- 检查会话是否存在
		local exists = redis.call('ZSCORE', key, sessionUUID)
		if exists ~= false then
			redis.call('ZADD', key, now, sessionUUID)
			redis.call('EXPIRE', key, idleTimeout + 60)
		end
		return 1
	`)

	// getActiveSessionCountScript prunes idle sessions and returns the remaining count.
	// KEYS[1] = session_limit:provider:{providerID}
	// ARGV[1] = idleTimeout in seconds.
	getActiveSessionCountScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local idleTimeout = tonumber(ARGV[1])

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - idleTimeout

		-- 清理过期会话
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)

		return redis.call('ZCARD', key)
	`)

	// isSessionActiveScript checks a session against the idle cutoff and removes it if expired.
	// KEYS[1] = session_limit:provider:{providerID}
	// ARGV[1] = idleTimeout in seconds.
	// ARGV[2] = sessionUUID.
	isSessionActiveScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local idleTimeout = tonumber(ARGV[1])
		local sessionUUID = ARGV[2]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - idleTimeout

		-- 获取会话的时间戳
		local score = redis.call('ZSCORE', key, sessionUUID)
		if score == false then
			return 0
		end

		-- 检查是否过期
		if tonumber(score) <= expireBefore then
			return 0
		end

		return 1
	`)
)

type sessionLimitCache struct {
	rdb                *redis.Client
	defaultIdleTimeout time.Duration // Default idle timeout for queries without a provider-specific value.
}

// NewSessionLimitCache creates Redis session storage.
// Nonpositive defaultIdleTimeoutMinutes uses five minutes for queries without an override.
func NewSessionLimitCache(rdb *redis.Client, defaultIdleTimeoutMinutes int) scheduler.SessionLimitCache {
	if defaultIdleTimeoutMinutes <= 0 {
		defaultIdleTimeoutMinutes = 5 // Five minutes.
	}

	// Warm the script cache for Run calls. Batch queries use EVAL and also work after a cache flush.
	ctx := context.Background()
	scripts := []*redis.Script{
		registerSessionScript,
		refreshSessionScript,
		getActiveSessionCountScript,
		isSessionActiveScript,
	}
	for _, script := range scripts {
		if err := script.Load(ctx, rdb).Err(); err != nil {
			log.Printf("[SessionLimitCache] Failed to preload Lua script: %v", err)
		}
	}

	return &sessionLimitCache{
		rdb:                rdb,
		defaultIdleTimeout: time.Duration(defaultIdleTimeoutMinutes) * time.Minute,
	}
}

// sessionLimitKey returns the provider's Redis session key.
func sessionLimitKey(providerID int64) string {
	return fmt.Sprintf("%s%d", sessionLimitKeyPrefix, providerID)
}

// RegisterSession records activity and reports admission; Redis errors return true together with the error.
func (c *sessionLimitCache) RegisterSession(ctx context.Context, providerID int64, sessionUUID string, maxSessions int, idleTimeout time.Duration) (bool, error) {
	if sessionUUID == "" || maxSessions <= 0 {
		return true, nil // An empty session or disabled limit requires no admission check.
	}

	key := sessionLimitKey(providerID)
	idleTimeoutSeconds := int(idleTimeout.Seconds())
	if idleTimeoutSeconds <= 0 {
		idleTimeoutSeconds = int(c.defaultIdleTimeout.Seconds())
	}

	result, err := registerSessionScript.Run(ctx, c.rdb, []string{key}, maxSessions, idleTimeoutSeconds, sessionUUID).Int()
	if err != nil {
		return true, err // Allow admission while reporting the cache error.
	}
	return result == 1, nil
}

// UnregisterSession removes a registration immediately.
// Failed attempts release registrations so they do not consume max_sessions capacity until idle expiry.
func (c *sessionLimitCache) UnregisterSession(ctx context.Context, providerID int64, sessionUUID string) error {
	if sessionUUID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, sessionLimitKey(providerID), sessionUUID).Err()
}

// RefreshSession updates an existing session's activity time.
func (c *sessionLimitCache) RefreshSession(ctx context.Context, providerID int64, sessionUUID string, idleTimeout time.Duration) error {
	if sessionUUID == "" {
		return nil
	}

	key := sessionLimitKey(providerID)
	idleTimeoutSeconds := int(idleTimeout.Seconds())
	if idleTimeoutSeconds <= 0 {
		idleTimeoutSeconds = int(c.defaultIdleTimeout.Seconds())
	}

	_, err := refreshSessionScript.Run(ctx, c.rdb, []string{key}, idleTimeoutSeconds, sessionUUID).Result()
	return err
}

// GetActiveSessionCount prunes idle sessions using the default timeout and returns the remaining count.
func (c *sessionLimitCache) GetActiveSessionCount(ctx context.Context, providerID int64) (int, error) {
	key := sessionLimitKey(providerID)
	idleTimeoutSeconds := int(c.defaultIdleTimeout.Seconds())

	result, err := getActiveSessionCountScript.Run(ctx, c.rdb, []string{key}, idleTimeoutSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

// GetActiveSessionCountBatch returns successful counts; failed providers are omitted and the error result is nil.
func (c *sessionLimitCache) GetActiveSessionCountBatch(ctx context.Context, providerIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	if len(providerIDs) == 0 {
		return make(map[int64]int), nil
	}

	results := make(map[int64]int, len(providerIDs))

	// Pipeline the per-provider queries.
	pipe := c.rdb.Pipeline()

	cmds := make(map[int64]*redis.Cmd, len(providerIDs))
	for _, providerID := range providerIDs {
		key := sessionLimitKey(providerID)
		// Use a positive provider-specific idle timeout when supplied.
		idleTimeout := c.defaultIdleTimeout
		if idleTimeouts != nil {
			if t, ok := idleTimeouts[providerID]; ok && t > 0 {
				idleTimeout = t
			}
		}
		idleTimeoutSeconds := int(idleTimeout.Seconds())
		cmds[providerID] = getActiveSessionCountScript.Eval(ctx, pipe, []string{key}, idleTimeoutSeconds)
	}

	// Read successful results even if other pipeline commands fail.
	_, _ = pipe.Exec(ctx)

	for providerID, cmd := range cmds {
		if result, err := cmd.Int(); err == nil {
			results[providerID] = result
		}
	}

	return results, nil
}

// IsSessionActive reports whether the session is within the default idle timeout.
func (c *sessionLimitCache) IsSessionActive(ctx context.Context, providerID int64, sessionUUID string) (bool, error) {
	if sessionUUID == "" {
		return false, nil
	}

	key := sessionLimitKey(providerID)
	idleTimeoutSeconds := int(c.defaultIdleTimeout.Seconds())

	result, err := isSessionActiveScript.Run(ctx, c.rdb, []string{key}, idleTimeoutSeconds, sessionUUID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}
