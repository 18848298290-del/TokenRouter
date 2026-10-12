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

// Concurrency slots use one Redis sorted set per provider or user, with request IDs as members and timestamps as scores.
// ZCARD counts members in O(1); ZREMRANGEBYSCORE removes expired slots.
// Counting a set needs one Redis call and avoids scanning separate per-request keys.
const (
	// Sorted-set key prefixes.
	// Format: concurrency:provider:{providerID}.
	providerSlotKeyPrefix = "concurrency:provider:"
	// Format: concurrency:user:{userID}.
	userSlotKeyPrefix = "concurrency:user:"
	// Format: concurrency:api_key:{apiKeyID}.
	apiKeySlotKeyPrefix       = "concurrency:api_key:"
	liveProviderSlotKeyPrefix = "concurrency:live:provider:"
	liveUserSlotKeyPrefix     = "concurrency:live:user:"
	liveAPIKeySlotKeyPrefix   = "concurrency:live:api_key:"
	// Short-lived API key ingress leases count client WebSocket connections separately from request turns.
	openAIWSIngressLeaseKeyPrefix  = "concurrency:openai_ws_ingress:api_key:"
	openAIWSIngressLeaseTTLSeconds = 60
	liveLeaseTTLSeconds            = 60
	// User wait counters use concurrency:wait:{userID}.
	waitQueueKeyPrefix = "concurrency:wait:"
	// Provider wait counters use wait:provider:{providerID}.
	providerWaitKeyPrefix = "wait:provider:"

	// Default slot lifetime in minutes; configuration may override it.
	defaultSlotTTLMinutes = 15

	// Active indexes let background cleanup locate candidates without scanning all slot keys.
	// Members are provider or user IDs; scores are expected expiry times in Redis Unix seconds.
	providerActiveIndexKey = "concurrency:provider:active_index" // ZSET of provider IDs scored by expected expiry in Unix seconds.
	userActiveIndexKey     = "concurrency:user:active_index"     // ZSET of user IDs scored by expected expiry in Unix seconds.

	// Batch sizes bound the work submitted in each cleanup operation.
	activeIndexCleanupBatchSize  = 1000
	activeIndexPipelineChunkSize = 500

	// Legacy wait keys are absent from the active index and can keep refreshing their TTL under traffic.
	// This marker records completion of their startup sweep.
	legacyWaitSweepMarkerKey = "concurrency:startup:legacy_wait_sweep:v1"
)

var (
	// acquireScript adds a slot when ordinary and Live slot counts are below the limit.
	// Redis TIME supplies the shared clock for all instances.
	// KEYS[1] = ordinary slot key; KEYS[2] = corresponding Live slot key.
	// ARGV[1] = maxConcurrency
	// ARGV[2] = TTL in seconds.
	// ARGV[3] = requestID
	// Returns {acquired, Redis Unix seconds} so the active index uses the same time without another TIME call.
	acquireScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local liveKey = KEYS[2]
		local maxConcurrency = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local requestID = ARGV[3]

		-- 使用 Redis 服务器时间，确保多实例时钟一致
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		-- 清理过期槽位
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)

		-- 检查是否已存在（支持重试场景刷新时间戳）
		local exists = redis.call('ZSCORE', key, requestID)
		if exists ~= false then
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
			return {1, now}
		end

		-- 检查是否达到并发上限
		local count = redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
		if count < maxConcurrency then
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
			return {1, now}
		end

		return {0, now}
	`)

	// getCountScript removes expired members and counts ordinary and Live slots.
	// Redis TIME supplies the expiry cutoff.
	// KEYS[1] = ordinary slot key; KEYS[2] = corresponding Live slot key.
	// ARGV[1] = TTL in seconds.
	getCountScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local liveKey = KEYS[2]
		local ttl = tonumber(ARGV[1])

		-- 使用 Redis 服务器时间
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)
		return redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
	`)

	acquireLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local providerRegular = KEYS[1]
		local providerLive = KEYS[2]
		local userRegular = KEYS[3]
		local userLive = KEYS[4]
		local apiLive = KEYS[5]
		local providerMax = tonumber(ARGV[1])
		local userMax = tonumber(ARGV[2])
		local ttl = tonumber(ARGV[3])
		local leaseID = ARGV[4]
		local replacing = tonumber(ARGV[5])
		local now = tonumber(redis.call('TIME')[1])
		local liveExpireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', providerLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', userLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', apiLive, '-inf', liveExpireBefore)
		if redis.call('ZSCORE', providerLive, leaseID) ~= false then
			return 1
		end
		local providerCount = redis.call('ZCARD', providerRegular) + redis.call('ZCARD', providerLive)
		local userCount = redis.call('ZCARD', userRegular) + redis.call('ZCARD', userLive)
		local allowance = 0
		if replacing == 1 then allowance = 1 end
		if providerMax > 0 and providerCount >= providerMax + allowance then return 0 end
		if userMax > 0 and userCount >= userMax + allowance then return 0 end
		redis.call('ZADD', providerLive, now, leaseID)
		redis.call('ZADD', userLive, now, leaseID)
		redis.call('ZADD', apiLive, now, leaseID)
		redis.call('EXPIRE', providerLive, ttl)
		redis.call('EXPIRE', userLive, ttl)
		redis.call('EXPIRE', apiLive, ttl)
		return 1
	`)

	refreshLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local ttl = tonumber(ARGV[1])
		local leaseID = ARGV[2]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		for _, key in ipairs(KEYS) do
			redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
			if redis.call('ZSCORE', key, leaseID) == false then return 0 end
		end
		for _, key in ipairs(KEYS) do
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// trackSlotScript records a slot for concurrency statistics.
	// KEYS[1] = sorted-set key.
	// ARGV[1] = TTL in seconds.
	// ARGV[2] = requestID.
	trackSlotScript = redis.NewScript(`
		-- 兼容 Redis 3.2-4.x：启用按效果复制，确保 TIME 调用正确同步。
		-- Redis 5.0 及以上默认按效果复制，因此该调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local requestID = ARGV[2]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZADD', key, now, requestID)
		redis.call('EXPIRE', key, ttl)
		return 1
	`)

	// acquireOpenAIWSIngressLeaseScript removes expired members and acquires or refreshes a lease in one Redis operation.
	acquireOpenAIWSIngressLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local maxConnections = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local leaseID = ARGV[3]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZSCORE', key, leaseID) ~= false then
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end
		if redis.call('ZCARD', key) < maxConnections then
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end
		return 0
	`)

	// refreshOpenAIWSIngressLeaseScript renews an existing member. A missing member means the connection
	// has lost its distributed slot and the caller needs to terminate it.
	refreshOpenAIWSIngressLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local leaseID = ARGV[2]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZSCORE', key, leaseID) == false then
			return 0
		end
		redis.call('ZADD', key, now, leaseID)
		redis.call('EXPIRE', key, ttl)
		return 1
	`)

	// incrementWaitScript increments below maxWait and refreshes the counter TTL on admission.
	// KEYS[1] = wait queue key
	// ARGV[1] = maxWait
	// ARGV[2] = TTL in seconds
	// Returns {admitted, Redis Unix seconds} for updating the active index without another TIME call.
	incrementWaitScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local current = redis.call('GET', KEYS[1])
		if current == false then
			current = 0
		else
			current = tonumber(current)
		end
		local now = tonumber(redis.call('TIME')[1])

		if current >= tonumber(ARGV[1]) then
			return {0, now}
		end

		redis.call('INCR', KEYS[1])

		-- Refresh TTL so long-running traffic doesn't expire active queue counters.
		redis.call('EXPIRE', KEYS[1], ARGV[2])

		return {1, now}
	`)

	// incrementProviderWaitScript increments the provider wait count and refreshes its TTL on admission.
	// Returns {admitted, Redis Unix seconds}, like incrementWaitScript.
	incrementProviderWaitScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local current = redis.call('GET', KEYS[1])
		if current == false then
			current = 0
		else
			current = tonumber(current)
		end
		local now = tonumber(redis.call('TIME')[1])

		if current >= tonumber(ARGV[1]) then
			return {0, now}
		end

		redis.call('INCR', KEYS[1])

		-- Refresh TTL so long-running traffic doesn't expire active queue counters.
		redis.call('EXPIRE', KEYS[1], ARGV[2])

		return {1, now}
	`)

	// decrementWaitScript decrements an existing positive count without creating a negative counter.
	decrementWaitScript = redis.NewScript(`
			local current = redis.call('GET', KEYS[1])
			if current ~= false and tonumber(current) > 0 then
				redis.call('DECR', KEYS[1])
			end
			return 1
		`)

	// cleanupExpiredSlotsScript removes expired members from one provider or user slot set.
	// KEYS[1] = sorted-set key.
	// ARGV[1] = TTL in seconds.
	cleanupExpiredSlotsScript = redis.NewScript(`
		-- 兼容 3.2-4.x：脚本使用 TIME，需启用按效果复制，确保写入能同步到从库。
		-- 5.0 及以上默认按效果复制；保留调用不改变行为。
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZCARD', key) == 0 then
			redis.call('DEL', key)
		else
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// startupCleanupSlotScript removes members outside the current process prefix from one slot key.
	// KEYS[1] = sorted-set key; ARGV[1] = current process prefix; ARGV[2] = slot TTL in seconds.
	// Returns {removed, remaining} so Go can update the index without rereading the slot set.
	startupCleanupSlotScript = redis.NewScript(`
		local key = KEYS[1]
		local activePrefix = ARGV[1]
		local slotTTL = tonumber(ARGV[2])
		local removed = 0
		local members = redis.call('ZRANGE', key, 0, -1)
		for _, member in ipairs(members) do
			if string.sub(member, 1, string.len(activePrefix)) ~= activePrefix then
				removed = removed + redis.call('ZREM', key, member)
			end
		end
		local remaining = redis.call('ZCARD', key)
		if remaining == 0 then
			redis.call('DEL', key)
		else
			redis.call('EXPIRE', key, slotTTL)
		end
		return {removed, remaining}
	`)

	providerSlotIndex = slotIndexSpec{indexKey: providerActiveIndexKey, slotKey: providerSlotKey, waitKey: providerWaitKey}
	userSlotIndex     = slotIndexSpec{indexKey: userActiveIndexKey, slotKey: userSlotKey, waitKey: waitQueueKey}
)

type concurrencyCache struct {
	rdb                 *redis.Client
	slotTTLSeconds      int // Slot lifetime in seconds.
	waitQueueTTLSeconds int // Wait counter lifetime in seconds.
}

// slotIndexSpec pairs an active index with its slot and wait-counter key builders.
// The two builders share a signature; their field names identify the key type.
type slotIndexSpec struct {
	indexKey string
	slotKey  func(int64) string
	waitKey  func(int64) string
}

type activeIndexLoad struct {
	id        int64
	member    string
	slotCount int
	waitCount int
}

// NewConcurrencyCache creates Redis concurrency storage.
// Nonpositive slotTTLMinutes uses 15 minutes.
// Nonpositive waitQueueTTLSeconds uses the slot lifetime in seconds.
func NewConcurrencyCache(rdb *redis.Client, slotTTLMinutes int, waitQueueTTLSeconds int) scheduler.ConcurrencyCache {
	if slotTTLMinutes <= 0 {
		slotTTLMinutes = defaultSlotTTLMinutes
	}
	if waitQueueTTLSeconds <= 0 {
		waitQueueTTLSeconds = slotTTLMinutes * 60
	}
	return &concurrencyCache{
		rdb:                 rdb,
		slotTTLSeconds:      slotTTLMinutes * 60,
		waitQueueTTLSeconds: waitQueueTTLSeconds,
	}
}

// providerSlotKey returns the Redis key for a provider's ordinary slots.
func providerSlotKey(providerID int64) string {
	return fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)
}

func userSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
}

func apiKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", apiKeySlotKeyPrefix, apiKeyID)
}

func liveProviderSlotKey(providerID int64) string {
	return fmt.Sprintf("%s%d", liveProviderSlotKeyPrefix, providerID)
}

func liveUserSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", liveUserSlotKeyPrefix, userID)
}

func liveAPIKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", liveAPIKeySlotKeyPrefix, apiKeyID)
}

func openAIWSIngressLeaseKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", openAIWSIngressLeaseKeyPrefix, apiKeyID)
}

func waitQueueKey(userID int64) string {
	return fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
}

func providerWaitKey(providerID int64) string {
	return fmt.Sprintf("%s%d", providerWaitKeyPrefix, providerID)
}

// redisUnixSeconds returns the Redis clock used to calculate index expiry across instances.
func (c *concurrencyCache) redisUnixSeconds(ctx context.Context) (int64, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TIME: %w", err)
	}
	return now.Unix(), nil
}

// touchActiveIndexAt adds an ID to the active index after a successful primary write.
// The score is an absolute expiry in Redis Unix seconds. Index failures are logged;
// subsequent release and cleanup operations can reconcile the index.
func (c *concurrencyCache) touchActiveIndexAt(ctx context.Context, indexKey string, id int64, expireAt int64) {
	if c == nil || c.rdb == nil || id <= 0 || expireAt <= 0 {
		return
	}
	if err := c.rdb.ZAdd(ctx, indexKey, redis.Z{
		Score:  float64(expireAt),
		Member: strconv.FormatInt(id, 10),
	}).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: touch active index %s for %d failed: %v", indexKey, id, err)
	}
}

func (c *concurrencyCache) refreshProviderActiveIndex(ctx context.Context, providerID int64) {
	c.refreshActiveIndex(ctx, providerActiveIndexKey, providerID, providerSlotKey(providerID), providerWaitKey(providerID))
}

func (c *concurrencyCache) refreshUserActiveIndex(ctx context.Context, userID int64) {
	c.refreshActiveIndex(ctx, userActiveIndexKey, userID, userSlotKey(userID), waitQueueKey(userID))
}

// refreshActiveIndex reconciles an index entry with Redis slot and wait counts.
// Slot release, wait-count reduction, and expired-slot cleanup call it to remove idle entries.
// Index maintenance failures are logged so the primary operation can continue.
func (c *concurrencyCache) refreshActiveIndex(ctx context.Context, indexKey string, id int64, slotKey, waitKey string) {
	if c == nil || c.rdb == nil || id <= 0 {
		return
	}
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: refresh active index %s for %d failed: %v", indexKey, id, err)
		return
	}

	load, err := c.readActiveLoadForKey(ctx, id, slotKey, waitKey, now)
	if err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: refresh active index %s for %d failed: %v", indexKey, id, err)
		return
	}
	member := strconv.FormatInt(id, 10)
	if load.slotCount == 0 && load.waitCount <= 0 {
		if err := c.rdb.ZRem(ctx, indexKey, member).Err(); err != nil {
			logger.LegacyPrintf("repository.concurrency", "Warning: remove active index member %s from %s failed: %v", member, indexKey, err)
		}
		return
	}

	ttlSeconds := c.activeIndexTTL(load.slotCount, load.waitCount)
	if ttlSeconds <= 0 {
		return
	}
	c.touchActiveIndexAt(ctx, indexKey, id, now+int64(ttlSeconds))
}

// activeIndexTTL returns the longer lifetime among slot and wait counters that still have load.
// The caller removes the index entry when both counts are zero.
func (c *concurrencyCache) activeIndexTTL(slotCount int, waitCount int) int {
	ttlSeconds := 0
	if slotCount > 0 {
		ttlSeconds = c.slotTTLSeconds
	}
	if waitCount > 0 && c.waitQueueTTLSeconds > ttlSeconds {
		ttlSeconds = c.waitQueueTTLSeconds
	}
	return ttlSeconds
}

// readActiveLoadForKey removes expired slot members and reads the ID's remaining slot and wait counts.
func (c *concurrencyCache) readActiveLoadForKey(ctx context.Context, id int64, slotKey, waitKey string, now int64) (activeIndexLoad, error) {
	cutoffTime := now - int64(c.slotTTLSeconds)
	pipe := c.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
	zcardCmd := pipe.ZCard(ctx, slotKey)
	getCmd := pipe.Get(ctx, waitKey)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return activeIndexLoad{}, fmt.Errorf("pipeline exec: %w", err)
	}

	waitCount := 0
	if v, err := getCmd.Int(); err == nil && v > 0 {
		waitCount = v
	}
	return activeIndexLoad{
		id:        id,
		member:    strconv.FormatInt(id, 10),
		slotCount: int(zcardCmd.Val()),
		waitCount: waitCount,
	}, nil
}

// readIndexLoads reads loads for provider or user index candidates in batches.
// Chunked pipelines reduce round trips while bounding commands per batch.
func (c *concurrencyCache) readIndexLoads(ctx context.Context, spec slotIndexSpec, members []string, now int64) ([]activeIndexLoad, []string, error) {
	loads := make([]activeIndexLoad, 0, len(members))
	staleMembers := make([]string, 0)
	candidates := make([]activeIndexLoad, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil || id <= 0 {
			staleMembers = append(staleMembers, member)
			continue
		}
		candidates = append(candidates, activeIndexLoad{id: id, member: member})
	}

	cutoffTime := now - int64(c.slotTTLSeconds)
	for start := 0; start < len(candidates); start += activeIndexPipelineChunkSize {
		end := min(start+activeIndexPipelineChunkSize, len(candidates))
		chunk := candidates[start:end]

		pipe := c.rdb.Pipeline()
		type loadCmd struct {
			activeIndexLoad
			zcardCmd *redis.IntCmd
			getCmd   *redis.StringCmd
		}
		cmds := make([]loadCmd, 0, len(chunk))
		for _, candidate := range chunk {
			slotKey := spec.slotKey(candidate.id)
			waitKey := spec.waitKey(candidate.id)
			pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
			cmds = append(cmds, loadCmd{
				activeIndexLoad: candidate,
				zcardCmd:        pipe.ZCard(ctx, slotKey),
				getCmd:          pipe.Get(ctx, waitKey),
			})
		}
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return nil, nil, fmt.Errorf("pipeline exec: %w", err)
		}
		for _, cmd := range cmds {
			waitCount := 0
			if v, err := cmd.getCmd.Int(); err == nil && v > 0 {
				waitCount = v
			}
			loads = append(loads, activeIndexLoad{
				id:        cmd.id,
				member:    cmd.member,
				slotCount: int(cmd.zcardCmd.Val()),
				waitCount: waitCount,
			})
		}
	}

	return loads, staleMembers, nil
}

// removeActiveIndexMembers removes stale index members and logs maintenance failures.
func (c *concurrencyCache) removeActiveIndexMembers(ctx context.Context, indexKey string, members []string) {
	if len(members) == 0 {
		return
	}
	args := make([]any, 0, len(members))
	for _, member := range members {
		args = append(args, member)
	}
	if err := c.rdb.ZRem(ctx, indexKey, args...).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: remove %d active index members from %s failed: %v", len(members), indexKey, err)
	}
}

// runScriptInt64Pair runs a Lua script returning two integers, such as {result, now} or {removed, remaining}.
func runScriptInt64Pair(ctx context.Context, rdb *redis.Client, script *redis.Script, keys []string, args ...any) (int64, int64, error) {
	raw, err := script.Run(ctx, rdb, keys, args...).Result()
	if err != nil {
		return 0, 0, err
	}
	first, err := redisScriptInt64At(raw, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("parse script value 0: %w", err)
	}
	second, err := redisScriptInt64At(raw, 1)
	if err != nil {
		return 0, 0, fmt.Errorf("parse script value 1: %w", err)
	}
	return first, second, nil
}

// Provider slot operations

func (c *concurrencyCache) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	key := providerSlotKey(providerID)
	// Use the script's Redis time for the index expiry.
	result, now, err := runScriptInt64Pair(ctx, c.rdb, acquireScript, []string{key, liveProviderSlotKey(providerID)}, maxConcurrency, c.slotTTLSeconds, requestID)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Index successful acquisitions so background cleanup can discover the provider.
		c.touchActiveIndexAt(ctx, providerActiveIndexKey, providerID, now+int64(c.slotTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error {
	key := providerSlotKey(providerID)
	if err := c.rdb.ZRem(ctx, key, requestID).Err(); err != nil {
		return err
	}
	// Remove the index entry when both remaining slot and wait counts are empty.
	c.refreshProviderActiveIndex(ctx, providerID)
	return nil
}

func (c *concurrencyCache) GetProviderConcurrency(ctx context.Context, providerID int64) (int, error) {
	key := providerSlotKey(providerID)
	// The script uses Redis TIME for expiry checks.
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveProviderSlotKey(providerID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

func (c *concurrencyCache) GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	if len(providerIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	type providerCmd struct {
		providerID int64
		zcardCmd   *redis.IntCmd
		liveCmd    *redis.IntCmd
	}
	cmds := make([]providerCmd, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		slotKey := providerSlotKeyPrefix + strconv.FormatInt(providerID, 10)
		liveKey := liveProviderSlotKeyPrefix + strconv.FormatInt(providerID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		cmds = append(cmds, providerCmd{
			providerID: providerID,
			zcardCmd:   pipe.ZCard(ctx, slotKey),
			liveCmd:    pipe.ZCard(ctx, liveKey),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	result := make(map[int64]int, len(providerIDs))
	for _, cmd := range cmds {
		result[cmd.providerID] = int(cmd.zcardCmd.Val() + cmd.liveCmd.Val())
	}
	return result, nil
}

// User slot operations

func (c *concurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	key := userSlotKey(userID)
	// Use the script's Redis time for the index expiry.
	result, now, err := runScriptInt64Pair(ctx, c.rdb, acquireScript, []string{key, liveUserSlotKey(userID)}, maxConcurrency, c.slotTTLSeconds, requestID)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Index successful acquisitions so startup cleanup can discover the user.
		c.touchActiveIndexAt(ctx, userActiveIndexKey, userID, now+int64(c.slotTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	key := userSlotKey(userID)
	if err := c.rdb.ZRem(ctx, key, requestID).Err(); err != nil {
		return err
	}
	// Reconcile the index with the remaining Redis load.
	c.refreshUserActiveIndex(ctx, userID)
	return nil
}

func (c *concurrencyCache) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	key := userSlotKey(userID)
	// The script uses Redis TIME for expiry checks.
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveUserSlotKey(userID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

// TrackAPIKeySlot records a slot for API key concurrency statistics.
func (c *concurrencyCache) TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	_, err := trackSlotScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds, requestID).Result()
	return err
}

// ReleaseAPIKeySlot removes an API key statistics slot.
func (c *concurrencyCache) ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

func (c *concurrencyCache) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || maxConnections <= 0 || leaseID == "" {
		return false, nil
	}
	result, err := acquireOpenAIWSIngressLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{openAIWSIngressLeaseKey(apiKeyID)},
		maxConnections,
		openAIWSIngressLeaseTTLSeconds,
		leaseID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || leaseID == "" {
		return false, nil
	}
	result, err := refreshOpenAIWSIngressLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{openAIWSIngressLeaseKey(apiKeyID)},
		openAIWSIngressLeaseTTLSeconds,
		leaseID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || leaseID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, openAIWSIngressLeaseKey(apiKeyID), leaseID).Err()
}

func (c *concurrencyCache) AcquireLiveLease(
	ctx context.Context,
	providerID int64,
	providerMax int,
	userID int64,
	userMax int,
	apiKeyID int64,
	leaseID string,
	replacingRegularSlots bool,
) (bool, error) {
	if c == nil || c.rdb == nil || providerID <= 0 || userID <= 0 || apiKeyID <= 0 || leaseID == "" {
		return false, nil
	}
	replacing := 0
	if replacingRegularSlots {
		replacing = 1
	}
	result, err := acquireLiveLeaseScript.Run(ctx, c.rdb, []string{
		providerSlotKey(providerID),
		liveProviderSlotKey(providerID),
		userSlotKey(userID),
		liveUserSlotKey(userID),
		liveAPIKeySlotKey(apiKeyID),
	}, providerMax, userMax, liveLeaseTTLSeconds, leaseID, replacing).Int()
	return result == 1, err
}

func (c *concurrencyCache) RefreshLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || leaseID == "" {
		return false, nil
	}
	result, err := refreshLiveLeaseScript.Run(ctx, c.rdb, []string{
		liveProviderSlotKey(providerID),
		liveUserSlotKey(userID),
		liveAPIKeySlotKey(apiKeyID),
	}, liveLeaseTTLSeconds, leaseID).Int()
	return result == 1, err
}

func (c *concurrencyCache) ReleaseLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || leaseID == "" {
		return nil
	}
	pipe := c.rdb.TxPipeline()
	pipe.ZRem(ctx, liveProviderSlotKey(providerID), leaseID)
	pipe.ZRem(ctx, liveUserSlotKey(userID), leaseID)
	pipe.ZRem(ctx, liveAPIKeySlotKey(apiKeyID), leaseID)
	_, err := pipe.Exec(ctx)
	return err
}

// GetAPIKeyConcurrencyBatch returns current API key slot counts in a batch.
func (c *concurrencyCache) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	if len(apiKeyIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	type apiKeyCmd struct {
		apiKeyID int64
		zcardCmd *redis.IntCmd
		liveCmd  *redis.IntCmd
	}
	cmds := make([]apiKeyCmd, 0, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		slotKey := apiKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		liveKey := liveAPIKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		cmds = append(cmds, apiKeyCmd{
			apiKeyID: apiKeyID,
			zcardCmd: pipe.ZCard(ctx, slotKey),
			liveCmd:  pipe.ZCard(ctx, liveKey),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	result := make(map[int64]int, len(apiKeyIDs))
	for _, cmd := range cmds {
		result[cmd.apiKeyID] = int(cmd.zcardCmd.Val() + cmd.liveCmd.Val())
	}
	return result, nil
}

// Wait queue operations

func (c *concurrencyCache) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	key := waitQueueKey(userID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, incrementWaitScript, []string{key}, maxWait, c.waitQueueTTLSeconds)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Waiters keep an ID active even when no slots remain, so cleanup can discover its wait counter.
		c.touchActiveIndexAt(ctx, userActiveIndexKey, userID, now+int64(c.waitQueueTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) DecrementWaitCount(ctx context.Context, userID int64) error {
	key := waitQueueKey(userID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	if err == nil {
		// Recheck whether the remaining load still needs an index entry.
		c.refreshUserActiveIndex(ctx, userID)
	}
	return err
}

// Provider wait queue operations

func (c *concurrencyCache) IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error) {
	key := providerWaitKey(providerID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, incrementProviderWaitScript, []string{key}, maxWait, c.waitQueueTTLSeconds)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Index provider waiters for load queries and cleanup.
		c.touchActiveIndexAt(ctx, providerActiveIndexKey, providerID, now+int64(c.waitQueueTTLSeconds))
	}
	return result == 1, nil
}

func (c *concurrencyCache) DecrementProviderWaitCount(ctx context.Context, providerID int64) error {
	key := providerWaitKey(providerID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	if err == nil {
		// Remove the index entry once both wait and slot counts are empty.
		c.refreshProviderActiveIndex(ctx, providerID)
	}
	return err
}

func (c *concurrencyCache) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	key := providerWaitKey(providerID)
	val, err := c.rdb.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return val, nil
}

func (c *concurrencyCache) GetProvidersLoadBatch(ctx context.Context, providers []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error) {
	if len(providers) == 0 {
		return map[int64]*scheduler.ProviderLoadInfo{}, nil
	}

	// Pipeline individual key operations to avoid cross-slot Lua access when reading multiple providers.
	// Read ordinary and Live slot counts plus wait counts after pruning expired slot members.
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	type providerCmds struct {
		id             int64
		maxConcurrency int
		zcardCmd       *redis.IntCmd
		liveCmd        *redis.IntCmd
		getCmd         *redis.StringCmd
	}
	cmds := make([]providerCmds, 0, len(providers))
	for _, acc := range providers {
		slotKey := providerSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		liveKey := liveProviderSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		waitKey := providerWaitKeyPrefix + strconv.FormatInt(acc.ID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		ac := providerCmds{
			id:             acc.ID,
			maxConcurrency: acc.MaxConcurrency,
			zcardCmd:       pipe.ZCard(ctx, slotKey),
			liveCmd:        pipe.ZCard(ctx, liveKey),
			getCmd:         pipe.Get(ctx, waitKey),
		}
		cmds = append(cmds, ac)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	loadMap := make(map[int64]*scheduler.ProviderLoadInfo, len(providers))
	for _, ac := range cmds {
		currentConcurrency := int(ac.zcardCmd.Val() + ac.liveCmd.Val())
		waitingCount := 0
		if v, err := ac.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if ac.maxConcurrency > 0 {
			loadRate = (currentConcurrency + waitingCount) * 100 / ac.maxConcurrency
		}
		loadMap[ac.id] = &scheduler.ProviderLoadInfo{
			ProviderID:         ac.id,
			CurrentConcurrency: currentConcurrency,
			WaitingCount:       waitingCount,
			LoadRate:           loadRate,
		}
	}

	return loadMap, nil
}

func (c *concurrencyCache) GetUsersLoadBatch(ctx context.Context, users []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error) {
	if len(users) == 0 {
		return map[int64]*scheduler.UserLoadInfo{}, nil
	}

	// Pipeline individual key operations when reading multiple users.
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	type userCmds struct {
		id             int64
		maxConcurrency int
		zcardCmd       *redis.IntCmd
		liveCmd        *redis.IntCmd
		getCmd         *redis.StringCmd
	}
	cmds := make([]userCmds, 0, len(users))
	for _, u := range users {
		slotKey := userSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		liveKey := liveUserSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		waitKey := waitQueueKeyPrefix + strconv.FormatInt(u.ID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		uc := userCmds{
			id:             u.ID,
			maxConcurrency: u.MaxConcurrency,
			zcardCmd:       pipe.ZCard(ctx, slotKey),
			liveCmd:        pipe.ZCard(ctx, liveKey),
			getCmd:         pipe.Get(ctx, waitKey),
		}
		cmds = append(cmds, uc)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	loadMap := make(map[int64]*scheduler.UserLoadInfo, len(users))
	for _, uc := range cmds {
		currentConcurrency := int(uc.zcardCmd.Val() + uc.liveCmd.Val())
		waitingCount := 0
		if v, err := uc.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if uc.maxConcurrency > 0 {
			loadRate = (currentConcurrency + waitingCount) * 100 / uc.maxConcurrency
		}
		loadMap[uc.id] = &scheduler.UserLoadInfo{
			UserID:             uc.id,
			CurrentConcurrency: currentConcurrency,
			WaitingCount:       waitingCount,
			LoadRate:           loadRate,
		}
	}

	return loadMap, nil
}

func (c *concurrencyCache) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	key := providerSlotKey(providerID)
	_, err := cleanupExpiredSlotsScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds).Result()
	if err == nil {
		// Reconcile the active index after cleaning one provider's slots.
		c.refreshProviderActiveIndex(ctx, providerID)
	}
	return err
}

// CleanupExpiredProviderSlotKeys reconciles expired candidates in both provider and user active indexes.
func (c *concurrencyCache) CleanupExpiredProviderSlotKeys(ctx context.Context) error {
	if err := c.reconcileExpiredIndexCandidates(ctx, providerSlotIndex); err != nil {
		return err
	}
	return c.reconcileExpiredIndexCandidates(ctx, userSlotIndex)
}

// reconcileExpiredIndexCandidates processes index members whose scores have expired.
// It removes idle members and refreshes scores for members with remaining load.
func (c *concurrencyCache) reconcileExpiredIndexCandidates(ctx context.Context, spec slotIndexSpec) error {
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return err
	}
	members, err := c.rdb.ZRangeByScore(ctx, spec.indexKey, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   strconv.FormatInt(now, 10),
		Count: activeIndexCleanupBatchSize,
	}).Result()
	if err != nil {
		return fmt.Errorf("read expired index %s: %w", spec.indexKey, err)
	}

	loads, staleMembers, err := c.readIndexLoads(ctx, spec, members, now)
	if err != nil {
		return err
	}
	refreshed := make([]redis.Z, 0, len(loads))
	for _, load := range loads {
		if load.slotCount == 0 && load.waitCount <= 0 {
			// No slots or waiters remain, so this index entry can be removed.
			staleMembers = append(staleMembers, load.member)
			continue
		}
		refreshed = append(refreshed, redis.Z{
			Score:  float64(now + int64(c.activeIndexTTL(load.slotCount, load.waitCount))),
			Member: load.member,
		})
	}
	if len(refreshed) > 0 {
		if err := c.rdb.ZAdd(ctx, spec.indexKey, refreshed...).Err(); err != nil {
			logger.LegacyPrintf("repository.concurrency", "Warning: refresh %d active index members in %s failed: %v", len(refreshed), spec.indexKey, err)
		}
	}
	c.removeActiveIndexMembers(ctx, spec.indexKey, staleMembers)
	return nil
}

// CleanupStaleProcessSlots removes slots outside the current process prefix at startup.
// It reads all indexed candidates, including expired scores and crash leftovers.
// A separate startup sweep handles legacy wait counters absent from the index.
// API key statistics slots expire through score pruning and key TTL,
// so they are excluded from startup cleanup.
func (c *concurrencyCache) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	if activeRequestPrefix == "" {
		return nil
	}
	if err := c.sweepLegacyWaitKeysOnce(ctx); err != nil {
		return err
	}
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return err
	}

	providerMembers, err := c.allIndexMembers(ctx, providerActiveIndexKey)
	if err != nil {
		return err
	}
	if err := c.cleanupStaleProcessSlotsForIndex(ctx, providerSlotIndex, providerMembers, activeRequestPrefix, now); err != nil {
		return err
	}

	userMembers, err := c.allIndexMembers(ctx, userActiveIndexKey)
	if err != nil {
		return err
	}
	return c.cleanupStaleProcessSlotsForIndex(ctx, userSlotIndex, userMembers, activeRequestPrefix, now)
}

// sweepLegacyWaitKeysOnce removes wait keys created before active indexing.
// Traffic can keep refreshing these keys, and the active index cannot discover them.
// A SCAN sweep handles them; a marker suppresses later sweeps while it exists.
// Write the marker after success so a failed sweep can retry on the next startup.
func (c *concurrencyCache) sweepLegacyWaitKeysOnce(ctx context.Context) error {
	exists, err := c.rdb.Exists(ctx, legacyWaitSweepMarkerKey).Result()
	if err != nil {
		return fmt.Errorf("check legacy wait sweep marker: %w", err)
	}
	if exists > 0 {
		return nil
	}
	for _, pattern := range []string{providerWaitKeyPrefix + "*", waitQueueKeyPrefix + "*"} {
		var cursor uint64
		for {
			keys, next, err := c.rdb.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return fmt.Errorf("scan legacy wait keys %s: %w", pattern, err)
			}
			if len(keys) > 0 {
				if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
					return fmt.Errorf("delete legacy wait keys: %w", err)
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	if err := c.rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err(); err != nil {
		return fmt.Errorf("set legacy wait sweep marker: %w", err)
	}
	return nil
}

// allIndexMembers returns every index member, including expired scores.
// Startup cleanup needs expired entries to find leftovers after a long shutdown.
func (c *concurrencyCache) allIndexMembers(ctx context.Context, indexKey string) ([]string, error) {
	members, err := c.rdb.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("read active index %s: %w", indexKey, err)
	}
	return members, nil
}

// cleanupStaleProcessSlotsForIndex reconciles each provider or user in the index.
// Each script touches one slot key; Go then deletes its wait counter.
// The script's remaining slot count determines whether the index entry is kept.
func (c *concurrencyCache) cleanupStaleProcessSlotsForIndex(
	ctx context.Context,
	spec slotIndexSpec,
	members []string,
	activeRequestPrefix string,
	now int64,
) error {
	staleMembers := make([]string, 0)
	refreshed := make([]redis.Z, 0)
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil || id <= 0 {
			staleMembers = append(staleMembers, member)
			continue
		}

		_, remaining, err := runScriptInt64Pair(ctx, c.rdb, startupCleanupSlotScript, []string{spec.slotKey(id)}, activeRequestPrefix, c.slotTTLSeconds)
		if err != nil {
			return fmt.Errorf("cleanup stale process slots %s: %w", spec.slotKey(id), err)
		}
		// Reset the wait counter during startup; remaining current-prefix slots determine whether the index entry stays.
		if err := c.rdb.Del(ctx, spec.waitKey(id)).Err(); err != nil {
			return fmt.Errorf("delete stale wait key %s: %w", spec.waitKey(id), err)
		}
		if remaining > 0 {
			refreshed = append(refreshed, redis.Z{
				Score:  float64(now + int64(c.slotTTLSeconds)),
				Member: member,
			})
		} else {
			staleMembers = append(staleMembers, member)
		}
	}
	if len(refreshed) > 0 {
		if err := c.rdb.ZAdd(ctx, spec.indexKey, refreshed...).Err(); err != nil {
			logger.LegacyPrintf("repository.concurrency", "Warning: refresh %d active index members in %s failed: %v", len(refreshed), spec.indexKey, err)
		}
	}
	c.removeActiveIndexMembers(ctx, spec.indexKey, staleMembers)
	return nil
}
