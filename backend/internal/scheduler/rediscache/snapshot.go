package rediscache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

const (
	schedulerBucketSetKey           = "sched:v5:buckets"
	schedulerOutboxWatermarkKey     = "sched:v5:outbox:watermark"
	schedulerProviderPrefix         = "sched:v5:provider:"
	schedulerProviderMetaPrefix     = "sched:v5:meta:"
	schedulerProviderLastUsedPrefix = "sched:v5:provider:last_used:"
	schedulerActivePrefix           = "sched:v5:active:"
	schedulerReadyPrefix            = "sched:v5:ready:"
	schedulerVersionPrefix          = "sched:v5:ver:"
	schedulerEpochPrefix            = "sched:v5:epoch:"
	schedulerRetiredPrefix          = "sched:v5:retired:"
	schedulerSnapshotPrefix         = "sched:v5:"

	defaultSchedulerSnapshotMGetChunkSize  = 128
	defaultSchedulerSnapshotWriteChunkSize = 256
	schedulerLastUsedUpdateChunkSize       = 256

	// snapshotGraceTTLSeconds is the expiry grace period for superseded snapshots.
	// Keep the old version briefly so readers that already obtained its ID can finish ZRANGE.
	snapshotGraceTTLSeconds = 60

	schedulerGroupLifecycleLockPrefix      = "sched:v5:group:lifecycle-lock:"
	schedulerGroupLifecycleOwnerTokenBytes = 16
)

var (
	updateSchedulerLastUsedScript = redis.NewScript(`
local updated = 0
for index = 1, #ARGV do
    local key_index = (index - 1) * 2 + 1
    local candidate = tonumber(ARGV[index])
    if candidate == nil then
        return redis.error_reply('invalid last_used value')
    end
    if redis.call('EXISTS', KEYS[key_index]) == 1 then
        local current = tonumber(redis.call('GET', KEYS[key_index + 1]))
        if current == nil or candidate > current then
            redis.call('SET', KEYS[key_index + 1], ARGV[index])
            updated = updated + 1
        end
    end
end
return updated
`)

	// Epochs identify bucket writer generations; retired keys persist the retirement state.
	// Capture, allocation, and activation validate these in Lua: -1 means retired, -2 means an invalid or mismatched epoch.
	// Checking at both allocation and activation catches retirement during snapshot writes.
	// Retire advances the epoch once; Reopen clears the marker while keeping that epoch, making repeated calls idempotent.
	captureBucketWriteTokenScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
    return -1
end

local currentEpoch = redis.call('GET', KEYS[1])
if currentEpoch == false then
    redis.call('SET', KEYS[1], '1')
    return 1
end

local parsedEpoch = tonumber(currentEpoch)
if parsedEpoch == nil or parsedEpoch < 1 then
    return -2
end
return parsedEpoch
`)

	allocateSnapshotVersionScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
    return -1
end

local currentEpoch = tonumber(redis.call('GET', KEYS[1]))
local expectedEpoch = tonumber(ARGV[1])
if currentEpoch == nil or expectedEpoch == nil or currentEpoch ~= expectedEpoch then
    return -2
end

return redis.call('INCR', KEYS[3])
`)

	retireBucketScript = redis.NewScript(`
local retired = redis.call('GET', KEYS[2])
local currentEpoch = tonumber(redis.call('GET', KEYS[1])) or 0

if retired == false then
    currentEpoch = currentEpoch + 1
    if currentEpoch < 1 then
        currentEpoch = 1
    end
    redis.call('SET', KEYS[1], tostring(currentEpoch))
    redis.call('SET', KEYS[2], tostring(currentEpoch))
elseif currentEpoch < 1 then
    currentEpoch = tonumber(retired) or 1
    redis.call('SET', KEYS[1], tostring(currentEpoch))
end

redis.call('SREM', KEYS[3], ARGV[1])
local currentActive = redis.call('GET', KEYS[5])
if currentActive ~= false then
    redis.call('EXPIRE', ARGV[2] .. currentActive, tonumber(ARGV[3]))
end
redis.call('DEL', KEYS[4], KEYS[5])
return currentEpoch
`)

	reopenBucketScript = redis.NewScript(`
local currentEpochRaw = redis.call('GET', KEYS[1])
local currentEpoch = tonumber(currentEpochRaw)
local retiredEpochRaw = redis.call('GET', KEYS[2])

if retiredEpochRaw == false then
    if currentEpochRaw == false then
        redis.call('SET', KEYS[1], '1')
        return 1
    end
    if currentEpoch == nil or currentEpoch < 1 then
        return -2
    end
    return currentEpoch
end

local retiredEpoch = tonumber(retiredEpochRaw)
if retiredEpoch == nil or retiredEpoch < 1 then
    return -2
end
if currentEpoch == nil or currentEpoch < retiredEpoch then
    currentEpoch = retiredEpoch
end

redis.call('SET', KEYS[1], tostring(currentEpoch))
redis.call('DEL', KEYS[2])
redis.call('SREM', KEYS[3], ARGV[1])
local currentActive = redis.call('GET', KEYS[5])
if currentActive ~= false then
    redis.call('EXPIRE', ARGV[2] .. currentActive, tonumber(ARGV[3]))
end
redis.call('DEL', KEYS[4], KEYS[5])
return currentEpoch
`)

	// Compare the owner token before deleting so a delayed release cannot remove a successor's lease.
	releaseGroupLifecycleLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

	// activateSnapshotScript conditionally switches the active snapshot version in one Redis operation.
	// Activation accepts versions at least as new as the current one, preventing version rollback.
	// The old snapshot receives a grace TTL for readers that already hold its version.
	//
	// KEYS[1] = active version key.
	// KEYS[2] = ready marker key.
	// KEYS[3] = bucket set key.
	// KEYS[4] = newly written snapshot key.
	// KEYS[5] = epoch key.
	// KEYS[6] = retired marker key.
	// ARGV[1] = new version string.
	// ARGV[2] = bucket string for SADD.
	// ARGV[3] = snapshot key prefix for locating the old version.
	// ARGV[4] = grace TTL in seconds.
	// ARGV[5] = writer epoch.
	//
	// Returns 1 when activated, 0 for an older version, -1 when retired, and -2 for an epoch mismatch.
	activateSnapshotScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[6]) == 1 then
    redis.call('DEL', KEYS[4])
    return -1
end

local currentEpoch = tonumber(redis.call('GET', KEYS[5]))
local expectedEpoch = tonumber(ARGV[5])
if currentEpoch == nil or expectedEpoch == nil or currentEpoch ~= expectedEpoch then
    redis.call('DEL', KEYS[4])
    return -2
end

local currentActive = redis.call('GET', KEYS[1])
local newVersion = tonumber(ARGV[1])

if currentActive ~= false then
	local curVersion = tonumber(currentActive)
	if curVersion and newVersion < curVersion then
		redis.call('DEL', KEYS[4])
		return 0
	end
end

redis.call('SET', KEYS[1], ARGV[1])
redis.call('SET', KEYS[2], '1')
redis.call('SADD', KEYS[3], ARGV[2])

if currentActive ~= false and currentActive ~= ARGV[1] then
	redis.call('EXPIRE', ARGV[3] .. currentActive, tonumber(ARGV[4]))
end

return 1
`)
)

type SnapshotCache struct {
	codec          SnapshotCodec
	rdb            *redis.Client
	mgetChunkSize  int
	writeChunkSize int
}

// SnapshotCacheOptions controls read and write batch sizes; nonpositive values use defaults.
type SnapshotCacheOptions struct {
	MGetChunkSize  int
	WriteChunkSize int
}

func NewSnapshotCache(rdb *redis.Client, codec SnapshotCodec, options ...SnapshotCacheOptions) *SnapshotCache {
	opts := SnapshotCacheOptions{MGetChunkSize: defaultSchedulerSnapshotMGetChunkSize, WriteChunkSize: defaultSchedulerSnapshotWriteChunkSize}
	if len(options) > 0 {
		if options[0].MGetChunkSize > 0 {
			opts.MGetChunkSize = options[0].MGetChunkSize
		}
		if options[0].WriteChunkSize > 0 {
			opts.WriteChunkSize = options[0].WriteChunkSize
		}
	}
	return &SnapshotCache{rdb: rdb, codec: codec, mgetChunkSize: opts.MGetChunkSize, writeChunkSize: opts.WriteChunkSize}
}

func (c *SnapshotCache) GetSnapshot(ctx context.Context, bucket scheduler.SchedulerBucket) ([]scheduler.SnapshotProvider, bool, error) {
	readyKey := schedulerBucketKey(schedulerReadyPrefix, bucket)
	readyVal, err := c.rdb.Get(ctx, readyKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if readyVal != "1" {
		return nil, false, nil
	}

	activeKey := schedulerBucketKey(schedulerActivePrefix, bucket)
	activeVal, err := c.rdb.Get(ctx, activeKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	snapshotKey := schedulerSnapshotKey(bucket, activeVal)
	ids, err := c.rdb.ZRange(ctx, snapshotKey, 0, -1).Result()
	if err != nil {
		return nil, false, err
	}
	if len(ids) == 0 {
		// Treat an empty snapshot as a cache miss so the caller can query the database.
		// A newly created group may have gained providers since its empty snapshot was written.
		return nil, false, nil
	}

	keys := make([]string, 0, len(ids))
	lastUsedKeys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, schedulerProviderMetaKey(id))
		lastUsedKeys = append(lastUsedKeys, schedulerLastUsedKey(id))
	}
	values, err := c.mgetChunked(ctx, keys)
	if err != nil {
		return nil, false, err
	}
	lastUsedValues, err := c.mgetChunked(ctx, lastUsedKeys)
	if err != nil {
		return nil, false, err
	}

	providers := make([]scheduler.SnapshotProvider, 0, len(values))
	for i, val := range values {
		if val == nil {
			return nil, false, nil
		}
		provider, err := c.codec.Decode(val)
		if err != nil {
			return nil, false, err
		}
		if err := c.applySchedulerLastUsed(provider, lastUsedValues[i]); err != nil {
			return nil, false, err
		}
		providers = append(providers, provider)
	}

	return providers, true, nil
}

func (c *SnapshotCache) CaptureBucketWriteToken(ctx context.Context, bucket scheduler.SchedulerBucket) (scheduler.SchedulerBucketWriteToken, error) {
	result, err := captureBucketWriteTokenScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
	}).Int64()
	if err != nil {
		return scheduler.SchedulerBucketWriteToken{}, err
	}
	if err := schedulerBucketWriteResultError(result, bucket); err != nil {
		return scheduler.SchedulerBucketWriteToken{}, err
	}
	return scheduler.SchedulerBucketWriteToken{Bucket: bucket, Epoch: result}, nil
}

func (c *SnapshotCache) RetireBucket(ctx context.Context, bucket scheduler.SchedulerBucket) error {
	snapshotKeyPrefix := fmt.Sprintf("%s%d:%s:%s:v", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode)
	result, err := retireBucketScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
		schedulerBucketSetKey,
		schedulerBucketKey(schedulerReadyPrefix, bucket),
		schedulerBucketKey(schedulerActivePrefix, bucket),
	}, bucket.String(), snapshotKeyPrefix, snapshotGraceTTLSeconds).Int64()
	if err != nil {
		return err
	}
	if result < 1 {
		return fmt.Errorf("retire scheduler bucket %s returned invalid epoch %d", bucket.String(), result)
	}
	return nil
}

func (c *SnapshotCache) ReopenBucket(ctx context.Context, bucket scheduler.SchedulerBucket) (scheduler.SchedulerBucketWriteToken, error) {
	snapshotKeyPrefix := fmt.Sprintf("%s%d:%s:%s:v", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode)
	result, err := reopenBucketScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
		schedulerBucketSetKey,
		schedulerBucketKey(schedulerReadyPrefix, bucket),
		schedulerBucketKey(schedulerActivePrefix, bucket),
	}, bucket.String(), snapshotKeyPrefix, snapshotGraceTTLSeconds).Int64()
	if err != nil {
		return scheduler.SchedulerBucketWriteToken{}, err
	}
	if err := schedulerBucketWriteResultError(result, bucket); err != nil {
		return scheduler.SchedulerBucketWriteToken{}, err
	}
	return scheduler.SchedulerBucketWriteToken{Bucket: bucket, Epoch: result}, nil
}

func (c *SnapshotCache) TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (scheduler.SchedulerGroupLifecycleLease, bool, error) {
	if groupID <= 0 {
		return scheduler.SchedulerGroupLifecycleLease{}, false, fmt.Errorf("%w: group id must be positive", scheduler.ErrSchedulerGroupLifecycleLeaseInvalid)
	}
	if ttl <= 0 {
		return scheduler.SchedulerGroupLifecycleLease{}, false, fmt.Errorf("%w: ttl must be positive", scheduler.ErrSchedulerGroupLifecycleLeaseInvalid)
	}
	ownerToken, err := newSchedulerGroupLifecycleOwnerToken()
	if err != nil {
		return scheduler.SchedulerGroupLifecycleLease{}, false, err
	}
	acquired, err := c.rdb.SetNX(ctx, schedulerGroupLifecycleLockKey(groupID), ownerToken, ttl).Result()
	if err != nil {
		return scheduler.SchedulerGroupLifecycleLease{}, false, err
	}
	if !acquired {
		return scheduler.SchedulerGroupLifecycleLease{}, false, nil
	}
	return scheduler.SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: ownerToken}, true, nil
}

func (c *SnapshotCache) ReleaseGroupLifecycleLease(ctx context.Context, lease scheduler.SchedulerGroupLifecycleLease) error {
	if !lease.ValidFor(lease.GroupID) {
		return scheduler.ErrSchedulerGroupLifecycleLeaseInvalid
	}
	result, err := releaseGroupLifecycleLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{schedulerGroupLifecycleLockKey(lease.GroupID)},
		lease.OwnerToken,
	).Int64()
	if err != nil {
		return err
	}
	if result == 0 {
		return fmt.Errorf("%w: group=%d", scheduler.ErrSchedulerGroupLifecycleLeaseLost, lease.GroupID)
	}
	if result != 1 {
		return fmt.Errorf("release scheduler group lifecycle lease returned %d", result)
	}
	return nil
}

func newSchedulerGroupLifecycleOwnerToken() (string, error) {
	raw := make([]byte, schedulerGroupLifecycleOwnerTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate scheduler group lifecycle owner token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (c *SnapshotCache) SetSnapshot(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, providers []scheduler.SnapshotProvider) error {
	_, err := c.SetSnapshotAndReturnProviderIDs(ctx, bucket, token, providers)
	return err
}

// SetSnapshotAndReturnProviderIDs publishes a snapshot and returns the ordered IDs successfully encoded and written.
// The IDs can be reused within the same rebuild batch after activation and writer-token checks complete.
func (c *SnapshotCache) SetSnapshotAndReturnProviderIDs(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, providers []scheduler.SnapshotProvider) ([]int64, error) {
	if !token.ValidFor(bucket) {
		return nil, fmt.Errorf("%w: bucket=%s", scheduler.ErrSchedulerBucketWriteFenced, bucket.String())
	}
	// Validate the writer token during version allocation and again before activation.
	version, err := c.allocateSnapshotVersion(ctx, bucket, token)
	if err != nil {
		return nil, err
	}
	providerIDs, err := c.writeSnapshotVersionAndReturnProviderIDs(ctx, bucket, version, providers)
	if err != nil {
		return nil, err
	}
	if err := c.activateSnapshotVersion(ctx, bucket, token, version); err != nil {
		return nil, err
	}
	return providerIDs, nil
}

// SetSnapshotByProviderIDs reuses IDs from a full write in the same rebuild batch.
// Each bucket allocates, writes, and activates its own version while reusing the provider JSON and global keys.
func (c *SnapshotCache) SetSnapshotByProviderIDs(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, providerIDs []int64) error {
	if !token.ValidFor(bucket) {
		return fmt.Errorf("%w: bucket=%s", scheduler.ErrSchedulerBucketWriteFenced, bucket.String())
	}
	version, err := c.allocateSnapshotVersion(ctx, bucket, token)
	if err != nil {
		return err
	}
	if err := c.writeSnapshotProviderIDs(ctx, bucket, version, providerIDs); err != nil {
		return err
	}
	return c.activateSnapshotVersion(ctx, bucket, token, version)
}

func (c *SnapshotCache) allocateSnapshotVersion(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken) (string, error) {
	result, err := allocateSnapshotVersionScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
		schedulerBucketKey(schedulerVersionPrefix, bucket),
	}, token.Epoch).Int64()
	if err != nil {
		return "", err
	}
	if err := schedulerBucketWriteResultError(result, bucket); err != nil {
		return "", err
	}
	return strconv.FormatInt(result, 10), nil
}

func (c *SnapshotCache) writeSnapshotVersionAndReturnProviderIDs(ctx context.Context, bucket scheduler.SchedulerBucket, version string, providers []scheduler.SnapshotProvider) ([]int64, error) {
	providerIDs, err := c.writeProviderIDs(ctx, providers)
	if err != nil {
		return nil, err
	}
	if err := c.writeSnapshotProviderIDs(ctx, bucket, version, providerIDs); err != nil {
		return nil, err
	}
	return providerIDs, nil
}

func (c *SnapshotCache) writeSnapshotProviderIDs(ctx context.Context, bucket scheduler.SchedulerBucket, version string, providerIDs []int64) error {
	members := schedulerSnapshotMembers(providerIDs)
	return c.writeSnapshotMembers(ctx, bucket, version, members)
}

func schedulerSnapshotMembers(providerIDs []int64) []redis.Z {
	if len(providerIDs) == 0 {
		return nil
	}
	// Ordinal scores retain database order. Redis ZADD gives duplicate IDs their final score,
	// matching snapshot members built directly from the provider slice.
	members := make([]redis.Z, 0, len(providerIDs))
	for idx, providerID := range providerIDs {
		members = append(members, redis.Z{
			Score:  float64(idx),
			Member: strconv.FormatInt(providerID, 10),
		})
	}
	return members
}

func (c *SnapshotCache) writeSnapshotMembers(ctx context.Context, bucket scheduler.SchedulerBucket, version string, members []redis.Z) error {
	if len(members) == 0 {
		return nil
	}
	snapshotKey := schedulerSnapshotKey(bucket, version)
	pipe := c.rdb.Pipeline()
	for start := 0; start < len(members); start += c.writeChunkSize {
		end := min(start+c.writeChunkSize, len(members))
		pipe.ZAdd(ctx, snapshotKey, members[start:end]...)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (c *SnapshotCache) activateSnapshotVersion(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, version string) error {
	snapshotKey := schedulerSnapshotKey(bucket, version)
	// Activation rechecks retirement and writer epoch while conditionally switching the version.
	// The Lua script accepts a version at least as new as the active one,
	// preventing concurrent writers from rolling the version back.
	// A grace TTL keeps the previous snapshot available to readers that already selected it.
	activeKey := schedulerBucketKey(schedulerActivePrefix, bucket)
	readyKey := schedulerBucketKey(schedulerReadyPrefix, bucket)
	snapshotKeyPrefix := fmt.Sprintf("%s%d:%s:%s:v", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode)

	keys := []string{
		activeKey,
		readyKey,
		schedulerBucketSetKey,
		snapshotKey,
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
	}
	args := []any{version, bucket.String(), snapshotKeyPrefix, snapshotGraceTTLSeconds, token.Epoch}

	result, err := activateSnapshotScript.Run(ctx, c.rdb, keys, args...).Int64()
	if err != nil {
		return err
	}
	return schedulerBucketWriteResultError(result, bucket)
}

func schedulerBucketWriteResultError(result int64, bucket scheduler.SchedulerBucket) error {
	switch result {
	case -1:
		return fmt.Errorf("%w: bucket=%s", scheduler.ErrSchedulerBucketRetired, bucket.String())
	case -2:
		return fmt.Errorf("%w: bucket=%s", scheduler.ErrSchedulerBucketWriteFenced, bucket.String())
	default:
		return nil
	}
}

func (c *SnapshotCache) GetProvider(ctx context.Context, providerID int64) (scheduler.SnapshotProvider, error) {
	id := strconv.FormatInt(providerID, 10)
	values, err := c.rdb.MGet(ctx, schedulerProviderKey(id), schedulerLastUsedKey(id)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) != 2 || values[0] == nil {
		return nil, nil
	}
	provider, err := c.codec.Decode(values[0])
	if err != nil {
		return nil, err
	}
	if err := c.applySchedulerLastUsed(provider, values[1]); err != nil {
		return nil, err
	}
	return provider, nil
}

func (c *SnapshotCache) SetProvider(ctx context.Context, provider scheduler.SnapshotProvider) error {
	if provider == nil || provider.SnapshotMetadata().ID <= 0 {
		return nil
	}
	providerIDs, err := c.writeProviderIDs(ctx, []scheduler.SnapshotProvider{provider})
	if err != nil {
		return err
	}
	if len(providerIDs) == 0 {
		return c.DeleteProvider(ctx, provider.SnapshotMetadata().ID)
	}
	return nil
}

func (c *SnapshotCache) DeleteProvider(ctx context.Context, providerID int64) error {
	if providerID <= 0 {
		return nil
	}
	id := strconv.FormatInt(providerID, 10)
	return c.rdb.Del(ctx, schedulerProviderKey(id), schedulerProviderMetaKey(id), schedulerLastUsedKey(id)).Err()
}

func (c *SnapshotCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	if len(updates) == 0 {
		return nil
	}

	pipe := c.rdb.Pipeline()
	queued := 0
	keys := make([]string, 0, schedulerLastUsedUpdateChunkSize*2)
	args := make([]any, 0, schedulerLastUsedUpdateChunkSize)
	queueBatch := func() {
		if len(args) == 0 {
			return
		}
		updateSchedulerLastUsedScript.Eval(ctx, pipe, keys, args...)
		queued++
		keys = make([]string, 0, schedulerLastUsedUpdateChunkSize*2)
		args = make([]any, 0, schedulerLastUsedUpdateChunkSize)
	}
	for id, usedAt := range updates {
		if id <= 0 {
			continue
		}
		millis, err := schedulerLastUsedMillis(usedAt)
		if err != nil {
			slog.Warn("scheduler cache removes provider with unencodable payload",
				"provider_id", id,
				"error", err,
			)
			idText := strconv.FormatInt(id, 10)
			pipe.Del(ctx, schedulerProviderKey(idText), schedulerProviderMetaKey(idText), schedulerLastUsedKey(idText))
			queued++
			continue
		}
		idText := strconv.FormatInt(id, 10)
		keys = append(keys, schedulerProviderKey(idText), schedulerLastUsedKey(idText))
		args = append(args, millis)
		if len(args) >= schedulerLastUsedUpdateChunkSize {
			queueBatch()
		}
	}
	queueBatch()
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (c *SnapshotCache) ListBuckets(ctx context.Context) ([]scheduler.SchedulerBucket, error) {
	raw, err := c.rdb.SMembers(ctx, schedulerBucketSetKey).Result()
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.SchedulerBucket, 0, len(raw))
	for _, entry := range raw {
		bucket, ok := scheduler.ParseSchedulerBucket(entry)
		if !ok {
			continue
		}
		out = append(out, bucket)
	}
	return out, nil
}

func (c *SnapshotCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	val, err := c.rdb.Get(ctx, schedulerOutboxWatermarkKey).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (c *SnapshotCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	return c.rdb.Set(ctx, schedulerOutboxWatermarkKey, strconv.FormatInt(id, 10), 0).Err()
}

func schedulerBucketKey(prefix string, bucket scheduler.SchedulerBucket) string {
	return fmt.Sprintf("%s%d:%s:%s", prefix, bucket.GroupID, bucket.Platform, bucket.Mode)
}

func schedulerGroupLifecycleLockKey(groupID int64) string {
	return schedulerGroupLifecycleLockPrefix + strconv.FormatInt(groupID, 10)
}

func schedulerSnapshotKey(bucket scheduler.SchedulerBucket, version string) string {
	return fmt.Sprintf("%s%d:%s:%s:v%s", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode, version)
}

func schedulerProviderKey(id string) string {
	return schedulerProviderPrefix + id
}

func schedulerProviderMetaKey(id string) string {
	return schedulerProviderMetaPrefix + id
}

func schedulerLastUsedKey(id string) string {
	return schedulerProviderLastUsedPrefix + id
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func schedulerLastUsedMillis(value time.Time) (int64, error) {
	if _, err := value.MarshalJSON(); err != nil {
		return 0, err
	}
	return value.UTC().UnixMilli(), nil
}

func (c *SnapshotCache) applySchedulerLastUsed(provider scheduler.SnapshotProvider, value any) error {
	if provider == nil || value == nil {
		return nil
	}
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case []byte:
		raw = string(typed)
	default:
		return fmt.Errorf("unexpected last_used cache type: %T", value)
	}
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid last_used cache value %q: %w", raw, err)
	}
	lastUsedAt := time.UnixMilli(millis).UTC()
	current, err := c.codec.LastUsedAt(provider)
	if err != nil {
		return err
	}
	if current == nil || lastUsedAt.After(*current) {
		return c.codec.SetLastUsedAt(provider, ptrTime(lastUsedAt))
	}
	return nil
}

func (c *SnapshotCache) writeProviderIDs(ctx context.Context, providers []scheduler.SnapshotProvider) ([]int64, error) {
	if len(providers) == 0 {
		return nil, nil
	}

	pipe := c.rdb.Pipeline()
	providerIDs := make([]int64, 0, len(providers))
	pending := 0
	flush := func() error {
		if pending == 0 {
			return nil
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		pipe = c.rdb.Pipeline()
		pending = 0
		return nil
	}

	for _, provider := range providers {
		fullPayload, metaPayload, err := c.codec.Encode(provider)
		if err != nil {
			slog.Warn("scheduler cache skips provider with unencodable payload",
				"provider_id", provider.SnapshotMetadata().ID,
				"error", err,
			)
			continue
		}

		id := strconv.FormatInt(provider.SnapshotMetadata().ID, 10)
		pipe.Set(ctx, schedulerProviderKey(id), fullPayload, 0)
		pipe.Set(ctx, schedulerProviderMetaKey(id), metaPayload, 0)
		// Leave LastUsedAt side keys untouched so a delayed rebuild cannot overwrite newer scheduling times.
		providerIDs = append(providerIDs, provider.SnapshotMetadata().ID)
		pending++
		if pending >= c.writeChunkSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}

	if err := flush(); err != nil {
		return nil, err
	}
	return providerIDs, nil
}

func (c *SnapshotCache) mgetChunked(ctx context.Context, keys []string) ([]any, error) {
	if len(keys) == 0 {
		return []any{}, nil
	}

	out := make([]any, 0, len(keys))
	chunkSize := c.mgetChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultSchedulerSnapshotMGetChunkSize
	}
	for start := 0; start < len(keys); start += chunkSize {
		end := min(start+chunkSize, len(keys))
		part, err := c.rdb.MGet(ctx, keys[start:end]...).Result()
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func (c *SnapshotCache) AcquireBucketLease(ctx context.Context, bucket scheduler.SchedulerBucket, ttl time.Duration) (*scheduler.BucketLease, bool, error) {
	return NewBucketLocks(c.rdb).AcquireBucketLease(ctx, bucket, ttl)
}
