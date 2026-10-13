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

// Ordinary provider/user slots and renewable Live leases share admission
// capacity but use different sorted sets and expiration windows. Request IDs
// identify ordinary members, Live lease IDs occupy provider/user/API-key
// sets, and wait counters add queue pressure to reported load. Redis TIME
// supplies score cutoffs so instances compare activity against the same
// clock.
//
// Lua admission resolves counts and writes before Go updates separate active
// indexes. Those indexes discover ordinary slots and waiters for maintenance;
// index failures are logged and their reads/writes can race with later
// acquisitions. Startup cleanup keeps only the supplied process prefix in
// indexed ordinary sets and resets wait counters, so callers need to account
// for other live process prefixes. Ingress connection leases have separate
// API-key keys and short renewal lifetimes.
//
// Command deadlines and transport retries follow the supplied client's
// policy. Script calls can execute again after an uncertain response; wait
// counters carry no request identity, so another execution can repeat an
// increment or decrement even within one adapter call.

// Concurrency namespaces separate ordinary slots, renewable leases and wait
// observations; TTLs and index batch limits control expiry pruning and
// discovery maintenance work.
const (
	// providerSlotKeyPrefix names ordinary provider request sets, whose
	// Redis-time scores are counted together with the corresponding Live lease
	// set.
	providerSlotKeyPrefix = "concurrency:provider:"
	// userSlotKeyPrefix names ordinary user request sets, separating user-wide
	// admission from provider capacity and API-key statistics.
	userSlotKeyPrefix = "concurrency:user:"
	// apiKeySlotKeyPrefix names ordinary API-key statistics sets, recorded
	// independently of the dedicated ingress-connection admission keys.
	apiKeySlotKeyPrefix = "concurrency:api_key:"
	// liveProviderSlotKeyPrefix names renewable provider lease sets counted with
	// ordinary provider slots during request admission.
	liveProviderSlotKeyPrefix = "concurrency:live:provider:"
	// liveUserSlotKeyPrefix names renewable user lease sets counted with
	// ordinary user slots during request admission.
	liveUserSlotKeyPrefix = "concurrency:live:user:"
	// liveAPIKeySlotKeyPrefix names renewable API-key lease sets included in
	// batch concurrency statistics for long-lived Live sessions.
	liveAPIKeySlotKeyPrefix = "concurrency:live:api_key:"
	// openAIWSIngressLeaseKeyPrefix names API-key connection admission sets,
	// separating client WebSocket ingress lifetime from ordinary request turns.
	openAIWSIngressLeaseKeyPrefix = "concurrency:openai_ws_ingress:api_key:"
	// openAIWSIngressLeaseTTLSeconds is the sixty-second inactivity window used
	// when acquiring, pruning and renewing ingress connection members.
	openAIWSIngressLeaseTTLSeconds = 60
	// liveLeaseTTLSeconds is the sixty-second inactivity window used to prune
	// and renew provider, user and API-key Live memberships.
	liveLeaseTTLSeconds = 60
	// waitQueueKeyPrefix names user queue counters, indexed with ordinary user
	// load so maintenance can discover waiting activity.
	waitQueueKeyPrefix = "concurrency:wait:"
	// providerWaitKeyPrefix names provider queue counters, combined with
	// occupied slots when reporting provider load and indexing cleanup
	// candidates.
	providerWaitKeyPrefix = "wait:provider:"

	// defaultSlotTTLMinutes supplies the ordinary request lifetime when
	// NewConcurrencyCache receives a nonpositive configured minute value.
	defaultSlotTTLMinutes = 15

	// providerActiveIndexKey stores provider IDs with expected ordinary-slot or
	// wait-counter expiry scores for cleanup discovery.
	providerActiveIndexKey = "concurrency:provider:active_index"
	// userActiveIndexKey stores user IDs with expected ordinary-slot or
	// wait-counter expiry scores for cleanup discovery.
	userActiveIndexKey = "concurrency:user:active_index"

	// activeIndexCleanupBatchSize bounds the due members read from one index in
	// a periodic reconciliation call.
	activeIndexCleanupBatchSize = 1000
	// activeIndexPipelineChunkSize bounds candidate identities per load-reading
	// pipeline while each candidate queues pruning and count operations.
	activeIndexPipelineChunkSize = 500

	// legacyWaitSweepMarkerKey persists successful completion of the startup
	// wait-prefix sweep, allowing a failed or absent-marker sweep to retry.
	legacyWaitSweepMarkerKey = "concurrency:startup:legacy_wait_sweep:v1"
)

// Redis scripts couple admission or renewal decisions to their member writes;
// providerSlotIndex and userSlotIndex select the matching ordinary-slot/wait
// keys for separate discovery maintenance.
var (
	// acquireScript prunes ordinary and Live scores, then admits a new request
	// below their combined limit or refreshes an existing ordinary member. It
	// returns admission and Redis seconds so Go can index confirmed writes.
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

	// getCountScript prunes ordinary members by the configured slot lifetime and
	// Live members by sixty seconds, then returns their combined count for
	// admission observations.
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

	// acquireLiveLeaseScript prunes three Live sets and checks provider/user
	// capacity including ordinary ZCARD values. A provider-side duplicate
	// returns success before renewal; replacement permits one extra member, and
	// new leases write all three Live sets.
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

	// refreshLiveLeaseScript visits provider, user and API-key Live sets in order,
	// pruning and checking each membership. A missing member stops before later
	// sets are visited; renewal starts after all three checks succeed.
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

	// trackSlotScript prunes and records an ordinary statistics member using
	// Redis seconds, refreshing the set TTL without an admission-capacity
	// comparison.
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

	// acquireOpenAIWSIngressLeaseScript prunes expired connection members and
	// admits a new ID below its API-key limit. Existing IDs refresh even when
	// the current set is full.
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

	// refreshOpenAIWSIngressLeaseScript prunes old connection scores before
	// renewing a present lease. A missing or pruned member returns zero so the
	// caller can recognize lost ownership.
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

	// incrementWaitScript admits another user waiter below maxWait and refreshes
	// the counter TTL. It returns admission and Redis seconds for subsequent
	// user-index discovery updates.
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

	// incrementProviderWaitScript admits another provider waiter below maxWait
	// and refreshes its counter TTL. It returns admission and Redis seconds for
	// subsequent provider-index discovery updates.
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

	// decrementWaitScript reduces a present positive wait counter while leaving
	// missing or zero counters unchanged, supporting cleanup of confirmed
	// admission counts.
	decrementWaitScript = redis.NewScript(`
			local current = redis.call('GET', KEYS[1])
			if current ~= false and tonumber(current) > 0 then
				redis.call('DECR', KEYS[1])
			end
			return 1
		`)

	// cleanupExpiredSlotsScript prunes one ordinary set by Redis activity age,
	// deletes an empty set and refreshes the key TTL for remaining members.
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

	// startupCleanupSlotScript removes ordinary members outside the supplied
	// current-process prefix, then returns removed/remaining counts for Go's
	// separate wait reset and index maintenance.
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

	// providerSlotIndex associates provider discovery members with ordinary
	// provider slot sets and provider wait counters for load-based maintenance.
	providerSlotIndex = slotIndexSpec{indexKey: providerActiveIndexKey, slotKey: providerSlotKey, waitKey: providerWaitKey}
	// userSlotIndex associates user discovery members with ordinary user slot
	// sets and user wait counters for load-based maintenance.
	userSlotIndex = slotIndexSpec{indexKey: userActiveIndexKey, slotKey: userSlotKey, waitKey: waitQueueKey}
)

// concurrencyCache applies Redis slot admission, lease renewal and indexed
// ordinary-load maintenance.
type concurrencyCache struct {
	// rdb is shared with callers and remains open through slot/lease release,
	// batch observations and discovery-index maintenance.
	rdb *redis.Client // shared concurrency storage client
	// slotTTLSeconds and waitQueueTTLSeconds come from NewConcurrencyCache;
	// activeIndexTTL chooses the longer configured lifetime for the kinds of
	// ordinary load still present.
	slotTTLSeconds      int // ordinary slot lifetime seconds
	waitQueueTTLSeconds int // wait counter lifetime seconds
}

// slotIndexSpec associates one discovery index with ordinary slot and
// wait-counter key builders.
type slotIndexSpec struct {
	// indexKey, slotKey and waitKey address the same provider or user identity;
	// maintenance reads both primary load kinds before removing or refreshing
	// that identity's discovery member.
	indexKey string             // provider or user discovery index
	slotKey  func(int64) string // ordinary slot key builder
	waitKey  func(int64) string // wait counter key builder
}

// activeIndexLoad associates an observed ordinary-load pair with its parsed
// and stored identity.
type activeIndexLoad struct {
	// id selects primary load keys while member retains the discovery set's text
	// for exact removal or rescheduling, including valid noncanonical numeric
	// spellings.
	id     int64  // parsed numeric entity identity
	member string // stored discovery member text
	// slotCount and waitCount describe ordinary slots and positive wait activity
	// used by activeIndexTTL; Live members use their own short-lived sets
	// outside this discovery calculation.
	slotCount int // remaining ordinary slot count
	waitCount int // positive parsed waiter count
}

// NewConcurrencyCache constructs ordinary slot, wait and renewable-lease
// storage over the caller's Redis client. Nonpositive slot minutes select
// fifteen minutes; nonpositive wait seconds use that selected slot lifetime.
func NewConcurrencyCache(rdb *redis.Client, slotTTLMinutes int, waitQueueTTLSeconds int) scheduler.ConcurrencyCache {
	if slotTTLMinutes <= 0 {
		slotTTLMinutes = defaultSlotTTLMinutes
	}
	if waitQueueTTLSeconds <= 0 {
		// Convert the selected minute lifetime to seconds for wait expiry.
		waitQueueTTLSeconds = slotTTLMinutes * 60
	}
	return &concurrencyCache{
		rdb: rdb,
		// Store ordinary lifetimes in the same seconds used by Redis scores.
		slotTTLSeconds:      slotTTLMinutes * 60,
		waitQueueTTLSeconds: waitQueueTTLSeconds,
	}
}

// providerSlotKey returns the ordinary request-member set key for one
// provider.
func providerSlotKey(providerID int64) string {
	return fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)
}

// userSlotKey returns the ordinary request-member set key for one user.
func userSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
}

// apiKeySlotKey returns the ordinary statistics-member set key for one API
// key.
func apiKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", apiKeySlotKeyPrefix, apiKeyID)
}

// liveProviderSlotKey returns the renewable Live-member set key for one
// provider.
func liveProviderSlotKey(providerID int64) string {
	return fmt.Sprintf("%s%d", liveProviderSlotKeyPrefix, providerID)
}

// liveUserSlotKey returns the renewable Live-member set key for one user.
func liveUserSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", liveUserSlotKeyPrefix, userID)
}

// liveAPIKeySlotKey returns the renewable Live-member statistics key for one
// API key.
func liveAPIKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", liveAPIKeySlotKeyPrefix, apiKeyID)
}

// openAIWSIngressLeaseKey returns the client-connection lease set key for one
// API key.
func openAIWSIngressLeaseKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", openAIWSIngressLeaseKeyPrefix, apiKeyID)
}

// waitQueueKey returns the user waiting-count key used by queue admission.
func waitQueueKey(userID int64) string {
	return fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
}

// providerWaitKey returns the provider waiting-count key used by queue
// admission.
func providerWaitKey(providerID int64) string {
	return fmt.Sprintf("%s%d", providerWaitKeyPrefix, providerID)
}

// redisUnixSeconds returns the shared Redis clock value used for discovery
// expiry scores.
func (c *concurrencyCache) redisUnixSeconds(ctx context.Context) (int64, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("redis TIME: %w", err)
	}
	return now.Unix(), nil
}

// touchActiveIndexAt records an entity's expected discovery expiry after a
// confirmed primary write. Nil clients or nonpositive identities/times are
// ignored; Redis indexing failures are logged, and later activity can retry
// discovery for an unindexed identity.
func (c *concurrencyCache) touchActiveIndexAt(ctx context.Context, indexKey string, id int64, expireAt int64) {
	if c == nil || c.rdb == nil || id <= 0 || expireAt <= 0 {
		return
	}
	// Log discovery write failure while retaining the already-completed primary
	// member or wait increment; subsequent activity can attempt indexing this
	// identity again.
	if err := c.rdb.ZAdd(ctx, indexKey, redis.Z{
		Score:  float64(expireAt),
		Member: strconv.FormatInt(id, 10),
	}).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: touch active index %s for %d failed: %v", indexKey, id, err)
	}
}

// refreshProviderActiveIndex reconciles this provider's discovery member with
// its remaining ordinary slots and waiters. Maintenance failures are logged
// by the shared helper so the completed release can still succeed.
func (c *concurrencyCache) refreshProviderActiveIndex(ctx context.Context, providerID int64) {
	c.refreshActiveIndex(ctx, providerActiveIndexKey, providerID, providerSlotKey(providerID), providerWaitKey(providerID))
}

// refreshUserActiveIndex reconciles this user's discovery member with its
// remaining ordinary slots and waiters. Maintenance failures are logged by
// the shared helper so the completed release can still succeed.
func (c *concurrencyCache) refreshUserActiveIndex(ctx context.Context, userID int64) {
	c.refreshActiveIndex(ctx, userActiveIndexKey, userID, userSlotKey(userID), waitQueueKey(userID))
}

// refreshActiveIndex updates one discovery member according to its ordinary
// slot and wait load, ignoring unusable clients or identities. Clock, load
// and index failures are logged while its caller's primary operation remains
// completed.
//
// The load read first prunes expired ordinary scores, then observes slots and
// positive wait count. An idle identity loses its discovery member; otherwise
// the configured lifetime of the present load kinds determines a new expiry
// score. These reads and index writes are separate Redis commands, so
// concurrent new activity can race with removal or rescheduling.
func (c *concurrencyCache) refreshActiveIndex(ctx context.Context, indexKey string, id int64, slotKey, waitKey string) {
	if c == nil || c.rdb == nil || id <= 0 {
		return
	}
	// Retain the discovery entry when a shared-clock read fails because a new
	// expiry cannot be computed from a confirmed Redis time sample.
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: refresh active index %s for %d failed: %v", indexKey, id, err)
		return
	}

	// Retain discovery when primary observation fails so unavailable load is not
	// treated as idle; any earlier pruning can remain completed.
	load, err := c.readActiveLoadForKey(ctx, id, slotKey, waitKey, now)
	if err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: refresh active index %s for %d failed: %v", indexKey, id, err)
		return
	}
	member := strconv.FormatInt(id, 10)
	// Remove an idle observed identity from discovery; ordinary load reads and
	// this mutation are separate commands, so a concurrent acquisition can race
	// with removal.
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

// activeIndexTTL returns the configured discovery lifetime for the ordinary
// load kinds that remain. With no positive slot or wait count it returns
// zero, letting the caller remove an idle member.
//
// Ordinary slots select slotTTLSeconds; positive wait counts can extend that
// result to waitQueueTTLSeconds when it is longer. This comparison keeps an
// indexed waiter discoverable after slots finish, and retains the longer
// configured window when both kinds remain. The result is a maintenance hint
// from the caller's observation, not a fresh read of each primary key's
// remaining TTL.
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

// readActiveLoadForKey prunes ordinary slot scores using the supplied Redis
// seconds and observes slots plus this identity's wait counter. Missing,
// nonpositive or unparsable waits become zero when the pipeline succeeds or
// reports redis.Nil; other pipeline errors abort the observation.
func (c *concurrencyCache) readActiveLoadForKey(ctx context.Context, id int64, slotKey, waitKey string, now int64) (activeIndexLoad, error) {
	cutoffTime := now - int64(c.slotTTLSeconds)
	pipe := c.rdb.Pipeline()
	// Prune ordinary scores before queuing their count so expired request
	// members cannot extend the configured discovery lifetime selected from this
	// observation.
	pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
	zcardCmd := pipe.ZCard(ctx, slotKey)
	getCmd := pipe.Get(ctx, waitKey)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return activeIndexLoad{}, fmt.Errorf("pipeline exec: %w", err)
	}

	waitCount := 0
	// Treat absent, unparsable or nonpositive waiting data as no positive
	// discovery load, keeping usable ordinary counts after a successful
	// pipeline.
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

// readIndexLoads reads ordinary slot/wait load for valid discovery members
// and returns malformed members separately for removal. The caller supplies
// one Redis time sample for all pruning cutoffs.
//
// Each queued result retains both parsed identity and original member text so
// command results and later index repairs stay attached to the correct
// candidate. Pipelines are bounded by candidate chunks, prune before counting
// slots, and normalize missing or invalid waits to zero. A pipeline error
// other than redis.Nil aborts the batch after any completed pruning.
//
// The pipeline reports its first command error, so an earlier missing wait
// key can conceal a later command failure. Count results use Val without
// checking individual errors; this path can therefore return a zero count
// from a failed command when the batch's reported error is redis.Nil.
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
	// Bound candidates per pipeline while retaining their original member text,
	// so result assembly and later removals stay linked to the scanned discovery
	// entries.
	for start := 0; start < len(candidates); start += activeIndexPipelineChunkSize {
		end := min(start+activeIndexPipelineChunkSize, len(candidates))
		chunk := candidates[start:end]

		pipe := c.rdb.Pipeline()
		// loadCmd retains one discovery candidate beside its queued ordinary-slot
		// and wait observations.
		type loadCmd struct {
			activeIndexLoad // associated parsed discovery identity
			// zcardCmd and getCmd are queued for the embedded activeIndexLoad
			// candidate, retaining the relationship between its stored discovery
			// identity and observed primary load.
			zcardCmd *redis.IntCmd    // queued ordinary member count
			getCmd   *redis.StringCmd // queued stored wait count
		}
		cmds := make([]loadCmd, 0, len(chunk))
		for _, candidate := range chunk {
			slotKey := spec.slotKey(candidate.id)
			waitKey := spec.waitKey(candidate.id)
			// Prune old ordinary scores before their queued count so expired request
			// members cannot keep this observed discovery candidate classified as
			// loaded.
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
			// Normalize missing, invalid or nonpositive wait observations to zero so
			// successful ordinary counts still determine whether this candidate has
			// discoverable load.
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

// removeActiveIndexMembers attempts to remove discovery members selected as
// stale by its caller. Empty input is ignored; Redis failures are logged
// while the completed load check remains usable for other candidates.
func (c *concurrencyCache) removeActiveIndexMembers(ctx context.Context, indexKey string, members []string) {
	if len(members) == 0 {
		return
	}
	args := make([]any, 0, len(members))
	for _, member := range members {
		args = append(args, member)
	}
	// Log discovery removal failure and continue the caller's maintenance pass;
	// primary load decisions have already completed and a later pass can retry
	// stale entries.
	if err := c.rdb.ZRem(ctx, indexKey, args...).Err(); err != nil {
		logger.LegacyPrintf("repository.concurrency", "Warning: remove %d active index members from %s failed: %v", len(members), indexKey, err)
	}
}

// runScriptInt64Pair invokes a Lua script and decodes its first two
// integer-like response elements. Script and array/conversion errors
// propagate so callers index only successfully parsed operation results.
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

// AcquireProviderSlot admits a new ordinary request below combined
// ordinary/Live capacity, or refreshes its existing member. Nonpositive
// limits reject new IDs; callers release confirmed ownership using the same
// request ID.
func (c *concurrencyCache) AcquireProviderSlot(ctx context.Context, providerID int64, maxConcurrency int, requestID string) (bool, error) {
	key := providerSlotKey(providerID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, acquireScript, []string{key, liveProviderSlotKey(providerID)}, maxConcurrency, c.slotTTLSeconds, requestID)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Index confirmed ordinary acquisition using the script's clock, keeping
		// discovery writes separate so maintenance failures do not reverse the
		// acquired provider member.
		c.touchActiveIndexAt(ctx, providerActiveIndexKey, providerID, now+int64(c.slotTTLSeconds))
	}
	return result == 1, nil
}

// ReleaseProviderSlot removes this request's ordinary provider member and
// reconciles discovery load afterward. Missing members are harmless; primary
// removal errors propagate, while index maintenance errors are logged.
func (c *concurrencyCache) ReleaseProviderSlot(ctx context.Context, providerID int64, requestID string) error {
	key := providerSlotKey(providerID)
	if err := c.rdb.ZRem(ctx, key, requestID).Err(); err != nil {
		return err
	}
	// Recheck ordinary slots and waiters after removing the request member so
	// discovery can retire an idle provider while later traffic may still race
	// with that observation.
	c.refreshProviderActiveIndex(ctx, providerID)
	return nil
}

// GetProviderConcurrency prunes ordinary and Live scores with their
// respective activity lifetimes and returns the combined count. This
// observation can change before the next atomic admission attempt.
func (c *concurrencyCache) GetProviderConcurrency(ctx context.Context, providerID int64) (int, error) {
	key := providerSlotKey(providerID)
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveProviderSlotKey(providerID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

// GetProviderConcurrencyBatch returns combined ordinary and Live counts keyed
// by provider ID, with an empty map for empty input. One Redis clock sample
// supplies distinct ordinary-slot and sixty-second Live cutoffs.
//
// Each provider keeps both queued count commands beside its identity, so the
// two observations sum into that provider's result. Pruning precedes counting
// within the pipeline; the batch is an observation, not a transaction with
// admission. Redis command failures abort the batch, while duplicate IDs
// address the same provider keys and result entry.
func (c *concurrencyCache) GetProviderConcurrencyBatch(ctx context.Context, providerIDs []int64) (map[int64]int, error) {
	if len(providerIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	// Use one Redis clock observation with the ordinary lifetime; Live pruning
	// below uses its shorter renewal window for the other counted set.
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	// providerCmd pairs a provider identity with its ordinary and Live count
	// commands.
	type providerCmd struct {
		// providerID, zcardCmd and liveCmd identify the same provider; the two
		// count commands remain attached to that ID until ordinary and Live results
		// are summed.
		providerID int64         // associated provider count identity
		zcardCmd   *redis.IntCmd // queued ordinary slot count
		liveCmd    *redis.IntCmd // queued Live lease count
	}
	cmds := make([]providerCmd, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		slotKey := providerSlotKeyPrefix + strconv.FormatInt(providerID, 10)
		liveKey := liveProviderSlotKeyPrefix + strconv.FormatInt(providerID, 10)
		// Prune both activity sets before queuing their counts so the result
		// combines current ordinary and Live membership under their respective
		// expiration windows.
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

// AcquireUserSlot admits a new ordinary request below combined ordinary/Live
// user capacity, or refreshes its existing member. Nonpositive limits reject
// new IDs; callers release confirmed ownership using the same request ID.
func (c *concurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	key := userSlotKey(userID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, acquireScript, []string{key, liveUserSlotKey(userID)}, maxConcurrency, c.slotTTLSeconds, requestID)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Index confirmed ordinary acquisition using the script's clock so startup
		// and periodic cleanup can find user load after request activity or a
		// crash.
		c.touchActiveIndexAt(ctx, userActiveIndexKey, userID, now+int64(c.slotTTLSeconds))
	}
	return result == 1, nil
}

// ReleaseUserSlot removes this request's ordinary user member and reconciles
// discovery load afterward. Missing members are harmless; primary removal
// errors propagate, while index maintenance errors are logged.
func (c *concurrencyCache) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	key := userSlotKey(userID)
	if err := c.rdb.ZRem(ctx, key, requestID).Err(); err != nil {
		return err
	}
	// Recheck ordinary slots and waiters after primary removal so user discovery
	// follows remaining request load; maintenance errors leave that completed
	// removal in place.
	c.refreshUserActiveIndex(ctx, userID)
	return nil
}

// GetUserConcurrency prunes ordinary and Live scores with their respective
// activity lifetimes and returns the combined user count. This observation
// can change before the next atomic admission attempt.
func (c *concurrencyCache) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	key := userSlotKey(userID)
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveUserSlotKey(userID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

// TrackAPIKeySlot records or refreshes an ordinary request member for API-key
// statistics without enforcing an admission limit. Callers release it using
// the same request ID after its counted request finishes.
func (c *concurrencyCache) TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	_, err := trackSlotScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds, requestID).Result()
	return err
}

// ReleaseAPIKeySlot removes the ordinary API-key statistics member for this
// request ID. Missing members are harmless, while Redis failures propagate to
// the caller coordinating request cleanup.
func (c *concurrencyCache) ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

// AcquireOpenAIWSIngressLease admits or refreshes an API-key connection
// member with a sixty-second lifetime. Unusable clients, nonpositive
// identities/limits or empty IDs return false; successful callers renew and
// release that lease ID.
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

// RefreshOpenAIWSIngressLease renews a present ingress member after pruning
// expired scores. Invalid cache/identity inputs return false, and a missing
// member reports false so the owner can recognize lost connection capacity.
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

// ReleaseOpenAIWSIngressLease removes the supplied API-key connection member.
// Invalid cache/identity inputs are ignored, and repeating a completed
// removal is harmless when that member is already absent.
func (c *concurrencyCache) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || apiKeyID <= 0 || leaseID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, openAIWSIngressLeaseKey(apiKeyID), leaseID).Err()
}

// AcquireLiveLease reserves provider, user and API-key Live memberships with
// the short lease lifetime. Invalid clients, IDs or empty lease identity
// return false; nonpositive provider/user limits bypass their capacity gates.
//
// Replacement permits one extra member while the caller still owns regular
// slots, which the caller releases after successful transfer. The script
// prunes Live scores but counts ordinary sets as stored. A provider-side
// duplicate returns success without refreshing the three memberships; callers
// use RefreshLiveLease for renewal and release all three memberships after
// the session ends.
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

// RefreshLiveLease checks memberships in provider, user and API-key order,
// pruning each visited Live set before checking. Use the acquisition IDs for
// renewal; nil caches, nil clients or empty lease IDs return false.
//
// A missing member stops the checks and reports lease loss, leaving later
// sets unvisited. Scores and TTLs renew after every membership check succeeds.
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

// ReleaseLiveLease removes this identity from the provider, user and API-key
// Live sets in one transaction. Nil caches or empty IDs are ignored; repeated
// removal succeeds for already absent memberships.
func (c *concurrencyCache) ReleaseLiveLease(ctx context.Context, providerID, userID, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || leaseID == "" {
		return nil
	}
	// Queue removal from all three lease sets before EXEC so a successful
	// release applies the provider, user and API-key membership cleanup
	// together.
	pipe := c.rdb.TxPipeline()
	pipe.ZRem(ctx, liveProviderSlotKey(providerID), leaseID)
	pipe.ZRem(ctx, liveUserSlotKey(userID), leaseID)
	pipe.ZRem(ctx, liveAPIKeySlotKey(apiKeyID), leaseID)
	_, err := pipe.Exec(ctx)
	return err
}

// GetAPIKeyConcurrencyBatch returns ordinary-plus-Live statistics counts
// keyed by API-key ID, with an empty map for empty input. One Redis time
// sample supplies the ordinary and short Live pruning windows.
//
// Each API-key identity retains its two queued count commands until results
// are assembled, keeping those counts attached to the corresponding result.
// Pruning precedes counting in the pipeline, and command errors abort the
// batch. This reports observed member counts; the dedicated ingress
// connection keys are counted by their separate admission scripts.
func (c *concurrencyCache) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	if len(apiKeyIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	// Use one Redis clock observation with the ordinary lifetime; Live pruning
	// below uses its shorter renewal window for the other counted set.
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	// apiKeyCmd pairs an API-key identity with its ordinary and Live statistics
	// commands.
	type apiKeyCmd struct {
		// apiKeyID, zcardCmd and liveCmd identify the same API key; queued ordinary
		// and Live results are combined into that identity's statistics entry.
		apiKeyID int64         // associated API-key count identity
		zcardCmd *redis.IntCmd // queued ordinary slot count
		liveCmd  *redis.IntCmd // queued Live lease count
	}
	cmds := make([]apiKeyCmd, 0, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		slotKey := apiKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		liveKey := liveAPIKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		// Prune both activity sets before queuing their counts so the result
		// combines current ordinary and Live membership under their respective
		// expiration windows.
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

// IncrementWaitCount records another user waiter below maxWait and refreshes
// its configured expiry. Nonpositive limits reject new increments; callers
// decrement a count only after receiving confirmed admission.
func (c *concurrencyCache) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	key := waitQueueKey(userID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, incrementWaitScript, []string{key}, maxWait, c.waitQueueTTLSeconds)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Index confirmed wait activity independently of slot occupancy so a user
		// with only queued requests remains discoverable for maintenance.
		c.touchActiveIndexAt(ctx, userActiveIndexKey, userID, now+int64(c.waitQueueTTLSeconds))
	}
	return result == 1, nil
}

// DecrementWaitCount reduces an existing positive user wait counter and then
// reconciles discovery load. Missing or zero counters stay unchanged; callers
// use it to release confirmed wait admission.
func (c *concurrencyCache) DecrementWaitCount(ctx context.Context, userID int64) error {
	key := waitQueueKey(userID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	if err == nil {
		// Reconcile discovery after the confirmed decrement so remaining ordinary
		// slots or positive waiters retain the entry, while an idle user can be
		// removed.
		c.refreshUserActiveIndex(ctx, userID)
	}
	return err
}

// Provider wait queue operations

// IncrementProviderWaitCount records another provider waiter below maxWait
// and refreshes its configured expiry. Nonpositive limits reject new
// increments; callers decrement a count only after receiving confirmed
// admission.
func (c *concurrencyCache) IncrementProviderWaitCount(ctx context.Context, providerID int64, maxWait int) (bool, error) {
	key := providerWaitKey(providerID)
	result, now, err := runScriptInt64Pair(ctx, c.rdb, incrementProviderWaitScript, []string{key}, maxWait, c.waitQueueTTLSeconds)
	if err != nil {
		return false, err
	}
	if result == 1 {
		// Record confirmed provider wait activity in discovery so cleanup can find
		// queue-only load after all ordinary provider members have finished.
		c.touchActiveIndexAt(ctx, providerActiveIndexKey, providerID, now+int64(c.waitQueueTTLSeconds))
	}
	return result == 1, nil
}

// DecrementProviderWaitCount reduces an existing positive provider wait
// counter and then reconciles discovery load. Missing or zero counters stay
// unchanged; callers use it to release confirmed wait admission.
func (c *concurrencyCache) DecrementProviderWaitCount(ctx context.Context, providerID int64) error {
	key := providerWaitKey(providerID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	if err == nil {
		// Reconcile discovery after the confirmed decrement so ordinary slots or
		// positive waiters retain the entry, while an idle provider can be removed.
		c.refreshProviderActiveIndex(ctx, providerID)
	}
	return err
}

// GetProviderWaitingCount returns the stored provider waiting count, with
// zero for a missing key. Redis and integer-conversion errors propagate so
// callers can distinguish missing load from unavailable observation.
func (c *concurrencyCache) GetProviderWaitingCount(ctx context.Context, providerID int64) (int, error) {
	key := providerWaitKey(providerID)
	val, err := c.rdb.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	// An absent wait key supplies a zero queue observation, letting load
	// consumers distinguish a provider with no stored waiters from a failed
	// read.
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return val, nil
}

// GetProvidersLoadBatch reports occupied ordinary/Live slots, stored waiter
// counts and integer load percentages for the supplied provider capacities.
// Empty input returns an empty map; nonpositive capacities produce a zero
// percentage. For duplicate IDs, later input entries replace earlier result
// entries and supply their own percentage denominator.
//
// One Redis clock sample drives the two score-pruning windows before
// per-provider commands read both slot sets and wait counters. Results remain
// paired with the provider's supplied capacity for the percentage
// calculation. Missing or unparsable waits contribute zero, while parsed
// counts are combined as stored. Integer division truncates fractional
// percentages and queue pressure can push the result above one hundred; the
// pipeline is an observation that admission later rechecks.
//
// Pipeline errors other than redis.Nil abort the batch. An earlier missing
// wait key can conceal later command errors, and counts use each command's
// Val result without rechecking its error.
func (c *concurrencyCache) GetProvidersLoadBatch(ctx context.Context, providers []scheduler.ProviderWithConcurrency) (map[int64]*scheduler.ProviderLoadInfo, error) {
	if len(providers) == 0 {
		return map[int64]*scheduler.ProviderLoadInfo{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	// Use one Redis clock observation with the ordinary lifetime; Live pruning
	// below uses its shorter renewal window for the other counted set.
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	// providerCmds retains one provider's supplied capacity beside its queued
	// slots and waiter observation.
	type providerCmds struct {
		// id, maxConcurrency, zcardCmd, liveCmd and getCmd describe one provider
		// observation; result assembly applies that provider's supplied denominator
		// to its own slot and waiting counts.
		id             int64            // associated provider load identity
		maxConcurrency int              // supplied load percentage denominator
		zcardCmd       *redis.IntCmd    // queued ordinary slot count
		liveCmd        *redis.IntCmd    // queued Live lease count
		getCmd         *redis.StringCmd // queued provider wait observation
	}
	cmds := make([]providerCmds, 0, len(providers))
	for _, acc := range providers {
		slotKey := providerSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		liveKey := liveProviderSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		waitKey := providerWaitKeyPrefix + strconv.FormatInt(acc.ID, 10)
		// Prune both activity sets before queuing their counts so the result
		// combines current ordinary and Live membership under their respective
		// expiration windows.
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
		// Keep parsed waiting values as observed and use zero when conversion
		// fails, allowing successful slot observations to contribute to this load
		// result.
		if v, err := ac.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if ac.maxConcurrency > 0 {
			// Combine occupied slots with queue pressure; integer division truncates
			// fractional load percentages.
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

// GetUsersLoadBatch reports occupied ordinary/Live user slots, stored waiter
// counts and integer load percentages for supplied capacities. Empty input
// returns an empty map; nonpositive capacities produce a zero percentage.
// For duplicate IDs, later input entries replace earlier results and supply
// their own percentage denominator.
//
// One Redis time sample provides ordinary-slot and short Live pruning cutoffs
// before queued commands read each user's load. Result commands stay paired
// with that user's capacity so their slot and wait totals use the correct
// denominator. Missing or unparsable waits contribute zero, while parsed
// counts are combined as stored. Integer division truncates fractional
// percentages and can report queue pressure above one hundred; admission
// remains a later operation.
//
// Pipeline errors other than redis.Nil abort the batch. An earlier missing
// wait key can conceal later command errors, and counts use each command's
// Val result without rechecking its error.
func (c *concurrencyCache) GetUsersLoadBatch(ctx context.Context, users []scheduler.UserWithConcurrency) (map[int64]*scheduler.UserLoadInfo, error) {
	if len(users) == 0 {
		return map[int64]*scheduler.UserLoadInfo{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	// Use one Redis clock observation with the ordinary lifetime; Live pruning
	// below uses its shorter renewal window for the other counted set.
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	// userCmds retains one user's supplied capacity beside its queued slots and
	// waiter observation.
	type userCmds struct {
		// id, maxConcurrency, zcardCmd, liveCmd and getCmd describe one user
		// observation; result assembly applies that user's supplied denominator to
		// its own slot and waiting counts.
		id             int64            // associated user load identity
		maxConcurrency int              // supplied load percentage denominator
		zcardCmd       *redis.IntCmd    // queued ordinary slot count
		liveCmd        *redis.IntCmd    // queued Live lease count
		getCmd         *redis.StringCmd // queued user wait observation
	}
	cmds := make([]userCmds, 0, len(users))
	for _, u := range users {
		slotKey := userSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		liveKey := liveUserSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		waitKey := waitQueueKeyPrefix + strconv.FormatInt(u.ID, 10)
		// Prune both activity sets before queuing their counts so the result
		// combines current ordinary and Live membership under their respective
		// expiration windows.
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
		// Keep parsed waiting values as observed and use zero when conversion
		// fails, allowing successful slot observations to contribute to this load
		// result.
		if v, err := uc.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if uc.maxConcurrency > 0 {
			// Combine occupied slots with queue pressure; integer division truncates
			// fractional load percentages.
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

// CleanupExpiredProviderSlots prunes one provider's expired ordinary members
// and adjusts key lifetime for the remainder. Successful pruning is followed
// by discovery reconciliation; errors from primary cleanup propagate.
func (c *concurrencyCache) CleanupExpiredProviderSlots(ctx context.Context, providerID int64) error {
	key := providerSlotKey(providerID)
	_, err := cleanupExpiredSlotsScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds).Result()
	if err == nil {
		// Read ordinary and waiting load after primary pruning so the discovery
		// score reflects what remains; index failures are logged while cleanup
		// stays completed.
		c.refreshProviderActiveIndex(ctx, providerID)
	}
	return err
}

// CleanupExpiredProviderSlotKeys reconciles bounded due candidates from
// provider discovery first, then user discovery. A provider-index failure
// stops this call before user work; unindexed primary keys remain outside
// these candidate queries.
func (c *concurrencyCache) CleanupExpiredProviderSlotKeys(ctx context.Context) error {
	if err := c.reconcileExpiredIndexCandidates(ctx, providerSlotIndex); err != nil {
		return err
	}
	return c.reconcileExpiredIndexCandidates(ctx, userSlotIndex)
}

// reconcileExpiredIndexCandidates reads a bounded set of discovery members
// whose scores are due and repairs their ordinary-load hints. Clock,
// index-query and load failures abort the call after any earlier primary
// pruning.
//
// Malformed identities and candidates with no positive load are selected for
// index removal. Loaded candidates receive expiry scores using their
// configured slot/wait lifetime; refresh and removal failures are logged so
// other candidates can continue. Primary counts and discovery mutation happen
// separately, so the result reflects an observation that concurrent
// acquisitions or releases can change.
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
			// Select an idle discovery member for removal from this observed load;
			// concurrent activity may begin before the separate index mutation runs.
			staleMembers = append(staleMembers, load.member)
			continue
		}
		refreshed = append(refreshed, redis.Z{
			Score:  float64(now + int64(c.activeIndexTTL(load.slotCount, load.waitCount))),
			Member: load.member,
		})
	}
	// Publish refreshed discovery scores after reading all selected primary
	// loads; indexing errors are logged so stale-member removal can still run in
	// this pass.
	if len(refreshed) > 0 {
		if err := c.rdb.ZAdd(ctx, spec.indexKey, refreshed...).Err(); err != nil {
			logger.LegacyPrintf("repository.concurrency", "Warning: refresh %d active index members in %s failed: %v", len(refreshed), spec.indexKey, err)
		}
	}
	c.removeActiveIndexMembers(ctx, spec.indexKey, staleMembers)
	return nil
}

// CleanupStaleProcessSlots removes indexed ordinary members outside
// activeRequestPrefix and resets their wait counters. Empty prefixes skip
// startup cleanup; API-key statistics and Live sets are outside this
// operation.
//
// A successful first-startup prefix sweep writes a persistent marker before
// indexed provider and user cleanup. Every index member is considered,
// including expired scores, so crash leftovers remain discoverable after a
// long shutdown. Prefix matching treats other processes' ordinary members as
// removable, even when that process is still live; callers account for this
// startup scope when sharing Redis between instances.
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

// sweepLegacyWaitKeysOnce scans and deletes both wait-key prefixes when the
// successful-sweep marker is absent. An existing marker skips scanning; a
// failure stops the sweep and leaves its marker unwritten.
//
// The prefixes include indexed and unindexed counters because old keys can
// keep refreshing under traffic and cannot all be discovered through an
// index. Deletions complete in cursor batches before the persistent marker is
// written. Concurrent starts can both see an absent marker, and current wait
// activity inside those prefixes can be removed; this marker records
// completion rather than providing mutual exclusion.
func (c *concurrencyCache) sweepLegacyWaitKeysOnce(ctx context.Context) error {
	exists, err := c.rdb.Exists(ctx, legacyWaitSweepMarkerKey).Result()
	if err != nil {
		return fmt.Errorf("check legacy wait sweep marker: %w", err)
	}
	if exists > 0 {
		return nil
	}
	// Sweep both wait prefixes before recording completion so unindexed legacy
	// counters are reached even when no discovery entry identifies their owner.
	for _, pattern := range []string{providerWaitKeyPrefix + "*", waitQueueKeyPrefix + "*"} {
		var cursor uint64
		for {
			// SCAN's count is a cursor-work hint for the wait-prefix sweep.
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
	// Write the persistent marker after every scan/delete batch succeeds so a
	// failed startup sweep can retry its undiscovered wait counters later.
	if err := c.rdb.Set(ctx, legacyWaitSweepMarkerKey, "1", 0).Err(); err != nil {
		return fmt.Errorf("set legacy wait sweep marker: %w", err)
	}
	return nil
}

// allIndexMembers returns all stored discovery members, including entries
// whose expiry scores are past. Startup cleanup needs that complete list to
// find ordinary process leftovers after a prolonged shutdown.
func (c *concurrencyCache) allIndexMembers(ctx context.Context, indexKey string) ([]string, error) {
	members, err := c.rdb.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("read active index %s: %w", indexKey, err)
	}
	return members, nil
}

// cleanupStaleProcessSlotsForIndex processes supplied provider/user discovery
// members using current-prefix ordinary cleanup and wait reset. Invalid
// member identities are retained for later index removal, and primary
// operation errors stop the sequence.
//
// The script's remaining ordinary count determines whether each discovery
// member is rescheduled after its wait key is deleted. A failure can leave
// earlier slot removal or wait resets completed; discovery refresh/removal
// runs only after candidate processing reaches the end. Index maintenance
// errors are logged, and separate primary/index commands leave room for
// concurrent activity to change the observed state.
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
		// Reset the wait counter after current-prefix slot cleanup so the script's
		// remaining ordinary count decides which startup discovery entries are
		// retained.
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
	// Publish retained-prefix discovery scores after primary cleanup reaches the
	// end; logged index failures do not restore removed ordinary members or
	// reset wait counters.
	if len(refreshed) > 0 {
		if err := c.rdb.ZAdd(ctx, spec.indexKey, refreshed...).Err(); err != nil {
			logger.LegacyPrintf("repository.concurrency", "Warning: refresh %d active index members in %s failed: %v", len(refreshed), spec.indexKey, err)
		}
	}
	c.removeActiveIndexMembers(ctx, spec.indexKey, staleMembers)
	return nil
}
