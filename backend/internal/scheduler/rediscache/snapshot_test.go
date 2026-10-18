package rediscache

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// A newer scheduling side timestamp can outlive the JSON record written by an
// earlier provider rebuild. Fixtures compare full and metadata bytes before
// and after hot updates, then exercise monotonic reads, stale payload
// replacement and missing-provider cleanup so last-used state keeps its
// intended provider association.
//
// Bucket membership and shared payloads have separate lifetimes. These cases
// check encoded-ID reuse, duplicate ordinals, empty or missing metadata,
// retirement epochs and delayed activation. Two barrier-based scenarios
// collect concurrent reopen or group-lock results using buffered channels;
// the assertions inspect returned tokens and owner identity after every
// operation result is collected.
//
// Payload benchmarks measure local encoding and member construction on fixed
// provider fixtures. Their aggregate totals combine payload byte lengths with
// member-entry counts, keeping the constructed results observable.

// schedulerCachePayloadBenchmarkSink retains encoded-byte and member-entry
// totals produced by each local payload benchmark iteration.
var schedulerCachePayloadBenchmarkSink int

// TestSchedulerCacheUpdateLastUsedUsesSideKeyWithoutRewritingPayloads updates
// a large provider's recent-use time after bucket publication. It checks
// unchanged full/metadata bytes, the side-key timestamp and newer times
// returned by full and bucket reads.
func TestSchedulerCacheUpdateLastUsedUsesSideKeyWithoutRewritingPayloads(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := scheduler.SchedulerBucket{
		GroupID:  9,
		Platform: capability.PlatformGrok,
		Mode:     scheduler.SchedulerModeSingle,
	}
	initial := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	provider := providercore.Record{
		ID:          9201,
		Name:        "grok-large-oauth",
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		LastUsedAt:  &initial,
		Credentials: map[string]any{
			"access_token":  strings.Repeat("a", 4096),
			"refresh_token": strings.Repeat("r", 4096),
		},
		Extra: map[string]any{"large": strings.Repeat("x", 4096)},
	}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider}))

	id := strconv.FormatInt(provider.ID, 10)
	fullBefore, err := cache.rdb.Get(ctx, schedulerProviderKey(id)).Bytes()
	require.NoError(t, err)
	metaBefore, err := cache.rdb.Get(ctx, schedulerProviderMetaKey(id)).Bytes()
	require.NoError(t, err)

	latest := initial.Add(37 * time.Second)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{provider.ID: latest}))

	fullAfter, err := cache.rdb.Get(ctx, schedulerProviderKey(id)).Bytes()
	require.NoError(t, err)
	metaAfter, err := cache.rdb.Get(ctx, schedulerProviderMetaKey(id)).Bytes()
	require.NoError(t, err)
	require.Equal(t, fullBefore, fullAfter)
	require.Equal(t, metaBefore, metaAfter)
	require.Equal(t, strconv.FormatInt(latest.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())

	cached, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.NotNil(t, cached.LastUsedAt)
	require.Equal(t, latest, *cached.LastUsedAt)

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.NotNil(t, snapshot[0].LastUsedAt)
	require.Equal(t, latest, *snapshot[0].LastUsedAt)
}

// TestSchedulerCacheLastUsedSideKeyIsMonotonicAndRequiresProvider sends newer
// then older observations to an existing provider. It checks retained newer
// time, no side key for a missing provider and side deletion when the
// provider is removed.
func TestSchedulerCacheLastUsedSideKeyIsMonotonicAndRequiresProvider(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	provider := providercore.Record{ID: 9202, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth}
	require.NoError(t, cache.SetProvider(ctx, &provider))

	newer := time.Now().UTC().Truncate(time.Millisecond)
	older := newer.Add(-time.Minute)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{provider.ID: newer}))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{provider.ID: older}))

	id := strconv.FormatInt(provider.ID, 10)
	require.Equal(t, strconv.FormatInt(newer.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())
	cached, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, newer, *cached.LastUsedAt)

	const missingID int64 = 9299
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{missingID: newer}))
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey(strconv.FormatInt(missingID, 10))).Result()
	require.ErrorIs(t, err, redis.Nil)

	require.NoError(t, cache.DeleteProvider(ctx, provider.ID))
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Result()
	require.ErrorIs(t, err, redis.Nil)
}

// TestSchedulerCacheLastUsedSideKeyFallsBackToNewerEmbeddedValue seeds a side
// timestamp older than the stored record. It checks that full lookup returns
// the later embedded scheduling time.
func TestSchedulerCacheLastUsedSideKeyFallsBackToNewerEmbeddedValue(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	embedded := time.Now().UTC().Truncate(time.Millisecond)
	provider := providercore.Record{
		ID:         9203,
		Platform:   capability.PlatformGrok,
		Type:       capability.ProviderTypeOAuth,
		LastUsedAt: &embedded,
	}
	require.NoError(t, cache.SetProvider(ctx, &provider))

	id := strconv.FormatInt(provider.ID, 10)
	require.NoError(t, cache.rdb.Set(ctx, schedulerLastUsedKey(id), embedded.Add(-time.Hour).UnixMilli(), 0).Err())
	cached, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, embedded, *cached.LastUsedAt)
}

// TestSchedulerCacheLastUsedSideKeySurvivesStaleProviderAndSnapshotWrites
// updates the side time before rewriting stale full and bucket payloads. It
// checks the side key survives and both read paths return its newer
// observation.
func TestSchedulerCacheLastUsedSideKeySurvivesStaleProviderAndSnapshotWrites(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := scheduler.SchedulerBucket{
		GroupID:  10,
		Platform: capability.PlatformGrok,
		Mode:     scheduler.SchedulerModeSingle,
	}
	embedded := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	latest := embedded.Add(30 * time.Second)
	provider := providercore.Record{
		ID:          9204,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Schedulable: true,
		LastUsedAt:  &embedded,
	}
	require.NoError(t, cache.SetProvider(ctx, &provider))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{provider.ID: latest}))

	require.NoError(t, cache.SetProvider(ctx, &provider))
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider}))

	id := strconv.FormatInt(provider.ID, 10)
	require.Equal(t, strconv.FormatInt(latest.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())
	cached, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, latest, *cached.LastUsedAt)
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, latest, *snapshot[0].LastUsedAt)
}

// TestSchedulerCacheUpdateLastUsedChunksLargeBatches creates one more
// provider than the Lua chunk limit and gives each a distinct time. It checks
// every side key receives its associated timestamp across the batch split.
func TestSchedulerCacheUpdateLastUsedChunksLargeBatches(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	// Cross the script chunk boundary by one provider so another chunk is
	// required; checking every distinct timestamp exposes misplaced key/argument
	// pairs despite unordered map iteration.
	total := schedulerLastUsedUpdateChunkSize + 1
	providers := make([]providercore.Record, 0, total)
	updates := make(map[int64]time.Time, total)
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i := range total {
		id := int64(9300 + i)
		providers = append(providers, providercore.Record{ID: id, Platform: capability.PlatformGrok})
		updates[id] = base.Add(time.Duration(i) * time.Millisecond)
	}

	written, err := cache.writeProviderIDs(ctx, providers)
	require.NoError(t, err)
	require.Len(t, written, total)
	require.NoError(t, cache.UpdateLastUsed(ctx, updates))

	for id, usedAt := range updates {
		key := schedulerLastUsedKey(strconv.FormatInt(id, 10))
		require.Equal(t, strconv.FormatInt(usedAt.UnixMilli(), 10), cache.rdb.Get(ctx, key).Val())
	}
}

// newSchedulerCacheUnit returns a provider-based snapshot cache backed by a
// fresh unit Redis fixture. The delegated fixture helper registers client
// cleanup and manages server lifetime through the current test.
func newSchedulerCacheUnit(t *testing.T) *schedulerCache {
	cache, _ := newSchedulerCacheUnitWithRedis(t)
	return cache
}

// newSchedulerCacheUnitWithRedis returns a cache and its miniredis server for
// controlled key expiry in unit tests. The test owns the server lifecycle and
// registers cleanup of the cache's client.
func newSchedulerCacheUnitWithRedis(t *testing.T) (*schedulerCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// Close the fixture client after the test's assertions finish; teardown
	// discards close errors while miniredis manages its own test-server cleanup.
	t.Cleanup(func() { _ = rdb.Close() })
	cache := newSchedulerCacheWithChunkSizes(rdb, defaultSchedulerSnapshotMGetChunkSize, defaultSchedulerSnapshotWriteChunkSize)
	return cache, mr
}

// TestSchedulerCacheWriteProviderIDsSkipsUnencodableTimes supplies one valid
// provider beside a record with an unencodable expiry. It checks only the
// valid ID returns and only its full record is retrievable.
func TestSchedulerCacheWriteProviderIDsSkipsUnencodableTimes(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	// Year ten thousand exceeds JSON's four-digit timestamp year format.
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	providerIDs, err := cache.writeProviderIDs(ctx, []providercore.Record{
		{ID: 111, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey},
		{ID: 112, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, ExpiresAt: &invalidTime},
	})
	require.NoError(t, err)
	require.Equal(t, []int64{111}, providerIDs)

	cached, err := cache.GetProvider(ctx, 111)
	require.NoError(t, err)
	require.NotNil(t, cached)

	invalid, err := cache.GetProvider(ctx, 112)
	require.NoError(t, err)
	require.Nil(t, invalid)
}

// TestSchedulerCacheSetProviderClearsUnencodablePayload first stores a valid
// record, then supplies an unencodable expiry update. It checks the operation
// returns successfully and subsequent full provider lookup misses.
func TestSchedulerCacheSetProviderClearsUnencodablePayload(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)

	provider := providercore.Record{ID: 113, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	require.NoError(t, cache.SetProvider(ctx, &provider))

	// Year ten thousand exceeds JSON's four-digit timestamp year format.
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	provider.ExpiresAt = &invalidTime
	require.NoError(t, cache.SetProvider(ctx, &provider))

	cached, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
}

// TestSchedulerCacheUpdateLastUsedClearsUnencodablePayload stores a provider
// before sending a recent-use time outside JSON's supported year range. It
// checks a successful update response followed by a full provider miss.
func TestSchedulerCacheUpdateLastUsedClearsUnencodablePayload(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	provider := providercore.Record{ID: 114, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	require.NoError(t, cache.SetProvider(ctx, &provider))

	// Year ten thousand exceeds JSON's four-digit timestamp year format.
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{provider.ID: invalidTime}))

	cached, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
}

// TestSchedulerCacheSnapshotProviderIDReusePreservesPayloadAndMembers mixes
// valid, unencodable and duplicate providers before reusing encoded IDs in
// another bucket. It checks fixed payload bytes, unchanged shared records,
// final duplicate ordinals and absence of the skipped provider.
func TestSchedulerCacheSnapshotProviderIDReusePreservesPayloadAndMembers(t *testing.T) {
	ctx := context.Background()
	cache, _ := newSchedulerCacheUnitWithRedis(t)
	// Year ten thousand exceeds JSON's four-digit timestamp year format.
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	validOne := providercore.Record{
		ID:          701,
		Name:        "first",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"z": "last", "a": "first"}},
		Extra:       map[string]any{"openai_passthrough": true},
		GroupIDs:    []int64{17},
	}
	validTwo := providercore.Record{ID: 702, Name: "second", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	invalid := providercore.Record{ID: 799, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, ExpiresAt: &invalidTime}
	providers := []providercore.Record{validOne, invalid, validTwo, validOne}

	single := scheduler.SchedulerBucket{GroupID: 17, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	providerIDs, err := cache.SetSnapshotAndReturnProviderIDs(ctx, single, singleToken, providers)
	require.NoError(t, err)
	require.Equal(t, []int64{701, 702, 701}, providerIDs, "应保留可编码提供商的原顺序和重复项")

	wantFull := historicalSchedulerPayload(t, "wantFull")
	wantMeta := historicalSchedulerPayload(t, "wantMeta")
	fullBefore, err := cache.rdb.Get(ctx, schedulerProviderKey("701")).Bytes()
	require.NoError(t, err)
	metaBefore, err := cache.rdb.Get(ctx, schedulerProviderMetaKey("701")).Bytes()
	require.NoError(t, err)
	require.Equal(t, wantFull, fullBefore)
	require.Equal(t, wantMeta, metaBefore)

	forced := scheduler.SchedulerBucket{GroupID: 17, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeForced}
	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByProviderIDs(ctx, forced, forcedToken, providerIDs))

	fullAfter, err := cache.rdb.Get(ctx, schedulerProviderKey("701")).Bytes()
	require.NoError(t, err)
	metaAfter, err := cache.rdb.Get(ctx, schedulerProviderMetaKey("701")).Bytes()
	require.NoError(t, err)
	require.Equal(t, fullBefore, fullAfter, "ID-only 路径不得重写完整提供商键")
	require.Equal(t, metaBefore, metaAfter, "ID-only 路径不得重写调度元数据键")

	for _, bucket := range []scheduler.SchedulerBucket{single, forced} {
		version, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerActivePrefix, bucket)).Result()
		require.NoError(t, err)
		members, err := cache.rdb.ZRange(ctx, schedulerSnapshotKey(bucket, version), 0, -1).Result()
		require.NoError(t, err)
		require.Equal(t, []string{"702", "701"}, members, bucket.String())
	}
	missing, err := cache.GetProvider(ctx, invalid.ID)
	require.NoError(t, err)
	require.Nil(t, missing)
}

// TestSchedulerCacheSetSnapshotMatchesIDPublishing compares SetSnapshot with
// SetSnapshotAndReturnProviderIDs on the same records. It checks identical
// payloads/member ordering and returned encoded IDs after the invalid-time
// provider is skipped.
func TestSchedulerCacheSetSnapshotMatchesIDPublishing(t *testing.T) {
	ctx := context.Background()
	cache, _ := newSchedulerCacheUnitWithRedis(t)
	// Year ten thousand exceeds JSON's four-digit timestamp year format.
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	validOne := providercore.Record{
		ID:          721,
		Name:        "first",
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"source": "target"}},
		Extra:       map[string]any{"openai_passthrough": true},
		GroupIDs:    []int64{21},
	}
	validTwo := providercore.Record{ID: 722, Name: "second", Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	invalid := providercore.Record{ID: 799, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, ExpiresAt: &invalidTime}
	providers := []providercore.Record{validOne, invalid, validTwo, validOne}

	normal := scheduler.SchedulerBucket{GroupID: 21, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	normalToken, err := cache.CaptureBucketWriteToken(ctx, normal)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, normal, normalToken, providers))

	fullBefore, err := cache.rdb.Get(ctx, schedulerProviderKey("721")).Bytes()
	require.NoError(t, err)
	metaBefore, err := cache.rdb.Get(ctx, schedulerProviderMetaKey("721")).Bytes()
	require.NoError(t, err)

	idOnly := scheduler.SchedulerBucket{GroupID: 21, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeForced}
	idOnlyToken, err := cache.CaptureBucketWriteToken(ctx, idOnly)
	require.NoError(t, err)
	providerIDs, err := cache.SetSnapshotAndReturnProviderIDs(ctx, idOnly, idOnlyToken, providers)
	require.NoError(t, err)
	require.Equal(t, []int64{721, 722, 721}, providerIDs)

	fullAfter, err := cache.rdb.Get(ctx, schedulerProviderKey("721")).Bytes()
	require.NoError(t, err)
	metaAfter, err := cache.rdb.Get(ctx, schedulerProviderMetaKey("721")).Bytes()
	require.NoError(t, err)
	require.Equal(t, fullBefore, fullAfter, "普通快照和 ID 发布必须写入相同完整提供商 payload")
	require.Equal(t, metaBefore, metaAfter, "普通快照和 ID 发布必须写入相同元数据 payload")

	for _, bucket := range []scheduler.SchedulerBucket{normal, idOnly} {
		version, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerActivePrefix, bucket)).Result()
		require.NoError(t, err)
		members, err := cache.rdb.ZRange(ctx, schedulerSnapshotKey(bucket, version), 0, -1).Result()
		require.NoError(t, err)
		require.Equal(t, []string{"722", "721"}, members, bucket.String())
	}
}

// TestSchedulerCacheSnapshotProviderIDReuseKeepsEmptySnapshotSemantics
// supplies an unencodable provider and reuses the resulting empty ID list. It
// checks both buckets become marked ready but return misses for their empty
// membership.
func TestSchedulerCacheSnapshotProviderIDReuseKeepsEmptySnapshotSemantics(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	// Year ten thousand exceeds JSON's four-digit timestamp year format.
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	providers := []providercore.Record{{ID: 811, Platform: capability.PlatformOpenAI, ExpiresAt: &invalidTime}}

	single := scheduler.SchedulerBucket{GroupID: 18, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	providerIDs, err := cache.SetSnapshotAndReturnProviderIDs(ctx, single, singleToken, providers)
	require.NoError(t, err)
	require.Empty(t, providerIDs)

	forced := scheduler.SchedulerBucket{GroupID: 18, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeForced}
	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByProviderIDs(ctx, forced, forcedToken, providerIDs))

	for _, bucket := range []scheduler.SchedulerBucket{single, forced} {
		ready, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerReadyPrefix, bucket)).Result()
		require.NoError(t, err)
		require.Equal(t, "1", ready)
		snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.False(t, hit, bucket.String())
		require.Nil(t, snapshot)
	}
}

// TestSchedulerCacheSetSnapshotByProviderIDsKeepsFencing calls the ID writer
// with an invalid token, then a token from a retired bucket. It checks
// rejection before version allocation and the retired-writer error.
func TestSchedulerCacheSetSnapshotByProviderIDsKeepsFencing(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := scheduler.SchedulerBucket{GroupID: 19, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeForced}

	err := cache.SetSnapshotByProviderIDs(ctx, bucket, scheduler.SchedulerBucketWriteToken{}, []int64{901})
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketWriteFenced)
	_, err = cache.rdb.Get(ctx, schedulerBucketKey(schedulerVersionPrefix, bucket)).Result()
	require.ErrorIs(t, err, redis.Nil)

	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	err = cache.SetSnapshotByProviderIDs(ctx, bucket, token, []int64{901})
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketRetired)
}

// TestSchedulerCacheSetSnapshotByProviderIDsDoesNotResurrectDeletedProvider
// removes shared data after an earlier encoded-ID write. It checks later
// ID-only membership leaves full lookup missing and the new bucket read
// misses on absent metadata.
func TestSchedulerCacheSetSnapshotByProviderIDsDoesNotResurrectDeletedProvider(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	provider := providercore.Record{ID: 902, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	single := scheduler.SchedulerBucket{GroupID: 20, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	providerIDs, err := cache.SetSnapshotAndReturnProviderIDs(ctx, single, singleToken, []providercore.Record{provider})
	require.NoError(t, err)
	require.Equal(t, []int64{provider.ID}, providerIDs)
	// Remove shared payloads after obtaining reusable IDs so the next bucket
	// publication exposes whether membership-only work accidentally recreates a
	// deleted provider.
	require.NoError(t, cache.DeleteProvider(ctx, provider.ID))

	forced := scheduler.SchedulerBucket{GroupID: 20, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeForced}
	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByProviderIDs(ctx, forced, forcedToken, providerIDs))

	full, err := cache.GetProvider(ctx, provider.ID)
	require.NoError(t, err)
	require.Nil(t, full, "ID-only 发布不得复活已删除的完整提供商键")
	snapshot, hit, err := cache.GetSnapshot(ctx, forced)
	require.NoError(t, err)
	require.False(t, hit, "元数据缺失时必须安全回源，而不是返回残缺快照")
	require.Nil(t, snapshot)
}

// TestBuildSchedulerMetadataProvider_KeepsQuotaStateForCachedProviders
// serializes total-quota exhaustion and active/expired daily, weekly, and monthly
// observations across provider types. It checks retained quota extras,
// filtered unrelated data and the resulting quota/schedulability decisions.
// Fixed monthly cases retain their reset date, hour, and timezone across encoding
// and reject or admit the cached provider using the captured observation time.
func TestBuildSchedulerMetadataProvider_KeepsQuotaStateForCachedProviders(t *testing.T) {
	now := time.Now().UTC()
	activeStart := now.Add(-time.Hour).Format(time.RFC3339)
	expiredDailyStart := now.Add(-25 * time.Hour).Format(time.RFC3339)
	expiredWeeklyStart := now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	weeklyResetDay := float64(now.AddDate(0, 0, 1).Weekday())
	monthlyTimezone, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	monthlyReset := billing.LastFixedMonthlyReset(15, 9, monthlyTimezone, now)

	cases := []struct {
		// name, platform, typ, extra and quotaExceeded form one cached-eligibility
		// case; the test copies the outer extra map before adding an unrelated
		// value for filtering assertions.
		name          string         // quota eligibility scenario name
		platform      string         // fixture provider platform
		typ           string         // provider authentication fixture type
		extra         map[string]any // configured quota state observations
		quotaExceeded bool           // expected quota exhaustion result
	}{
		{
			name: "anthropic api key total quota exhausted", platform: capability.PlatformAnthropic, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{"quota_limit": 10.0, "quota_used": 10.0}, quotaExceeded: true,
		},
		{
			name: "gemini api key rolling daily quota exhausted", platform: capability.PlatformGemini, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{
				"quota_daily_limit": 20.0, "quota_daily_used": 20.0,
				"quota_daily_start": activeStart, "quota_daily_reset_mode": "rolling",
			}, quotaExceeded: true,
		},
		{
			name: "gemini api key expired rolling daily window", platform: capability.PlatformGemini, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{
				"quota_daily_limit": 20.0, "quota_daily_used": 20.0,
				"quota_daily_start": expiredDailyStart, "quota_daily_reset_mode": "rolling",
			},
		},
		{
			name: "bedrock fixed weekly quota exhausted", platform: capability.PlatformAnthropic, typ: capability.ProviderTypeBedrock,
			extra: map[string]any{
				"quota_weekly_limit": 30.0, "quota_weekly_used": 30.0, "quota_weekly_start": activeStart,
				"quota_weekly_reset_mode": "fixed", "quota_weekly_reset_day": weeklyResetDay,
				"quota_weekly_reset_hour": 0.0, "quota_reset_timezone": "UTC",
			}, quotaExceeded: true,
		},
		{
			name: "bedrock expired fixed weekly window", platform: capability.PlatformAnthropic, typ: capability.ProviderTypeBedrock,
			extra: map[string]any{
				"quota_weekly_limit": 30.0, "quota_weekly_used": 30.0, "quota_weekly_start": expiredWeeklyStart,
				"quota_weekly_reset_mode": "fixed", "quota_weekly_reset_day": weeklyResetDay,
				"quota_weekly_reset_hour": 0.0, "quota_reset_timezone": "UTC",
			},
		},
		{
			name: "monthly quota exhausted", platform: capability.PlatformOpenAI, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{
				"quota_monthly_limit": 100.0, "quota_monthly_used": 100.0, "quota_monthly_start": activeStart,
			}, quotaExceeded: true,
		},
		{
			name: "expired monthly quota", platform: capability.PlatformOpenAI, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{
				"quota_monthly_limit": 100.0, "quota_monthly_used": 100.0,
				"quota_monthly_start": time.Now().Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339),
			},
		},
		{
			name: "fixed monthly quota exhausted", platform: capability.PlatformOpenAI, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{
				"quota_monthly_limit": 100.0, "quota_monthly_used": 100.0,
				"quota_monthly_start":      monthlyReset.UTC().Format(time.RFC3339),
				"quota_monthly_reset_mode": "fixed", "quota_monthly_reset_day": 15.0,
				"quota_monthly_reset_hour": 9.0, "quota_reset_timezone": "Asia/Shanghai",
			}, quotaExceeded: true,
		},
		{
			name: "expired fixed monthly quota", platform: capability.PlatformOpenAI, typ: capability.ProviderTypeAPIKey,
			extra: map[string]any{
				"quota_monthly_limit": 100.0, "quota_monthly_used": 100.0,
				"quota_monthly_start":      monthlyReset.Add(-time.Hour).UTC().Format(time.RFC3339),
				"quota_monthly_reset_mode": "fixed", "quota_monthly_reset_day": 15.0,
				"quota_monthly_reset_hour": 9.0, "quota_reset_timezone": "Asia/Shanghai",
			},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := make(map[string]any, len(tc.extra)+1)
			maps.Copy(extra, tc.extra)
			extra["unrelated"] = "drop me"
			provider := providercore.Record{
				ID: int64(46690 + i), Platform: tc.platform, Type: tc.typ, Extra: extra,
				Status: billing.StatusActive, Schedulable: true,
			}
			cache := newSchedulerCacheUnit(t)
			ctx := context.Background()
			bucket := scheduler.SchedulerBucket{GroupID: int64(46690 + i), Platform: tc.platform, Mode: scheduler.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider}))

			snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit)
			require.Len(t, snapshot, 1)
			cached := snapshot[0]
			// Fixed schedules use the captured observation time so a calendar reset
			// during serialization cannot change this fixture's expected admission result.
			cached.Now = func() time.Time { return now }
			require.Equal(t, tc.extra, cached.Extra)
			require.NotContains(t, cached.Extra, "unrelated")
			require.Equal(t, tc.quotaExceeded, cached.IsQuotaExceeded())
			require.Equal(t, !tc.quotaExceeded, cached.IsSchedulable())
		})
	}
}

// TestSchedulerCacheBucketRetirementFencesWritersAndReopen publishes two
// buckets before retiring one and later reopening it. It checks identity
// fencing, stable repeated retirement, grace expiry, unaffected peer state
// and new-generation publication.
func TestSchedulerCacheBucketRetirementFencesWritersAndReopen(t *testing.T) {
	ctx := context.Background()
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	bucket := scheduler.SchedulerBucket{GroupID: 41, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	otherBucket := scheduler.SchedulerBucket{GroupID: 42, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeSingle}
	provider := providercore.Record{ID: 4101, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}

	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.True(t, token.ValidFor(bucket))
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider}))

	// Reuse a token in another bucket before that bucket allocates its own
	// version; rejection must leave the peer's version key absent.
	err = cache.SetSnapshot(ctx, otherBucket, token, []providercore.Record{provider})
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketWriteFenced)
	_, err = cache.rdb.Get(ctx, schedulerBucketKey(schedulerVersionPrefix, otherBucket)).Result()
	require.ErrorIs(t, err, redis.Nil)
	otherProvider := providercore.Record{ID: 4201, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}
	otherToken, err := cache.CaptureBucketWriteToken(ctx, otherBucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, otherBucket, otherToken, []providercore.Record{otherProvider}))
	otherEpoch := otherToken.Epoch

	activeVersion, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerActivePrefix, bucket)).Result()
	require.NoError(t, err)
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	retiredEpoch, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerEpochPrefix, bucket)).Int64()
	require.NoError(t, err)
	require.Greater(t, retiredEpoch, token.Epoch)

	require.NoError(t, cache.RetireBucket(ctx, bucket))
	retiredEpochAgain, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerEpochPrefix, bucket)).Int64()
	require.NoError(t, err)
	require.Equal(t, retiredEpoch, retiredEpochAgain)

	// Inspect the new-reader miss beside the still-readable prior membership and
	// its bounded TTL, checking retirement's separate marker and retained-set
	// effects.
	_, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
	ids, err := cache.rdb.ZRange(ctx, schedulerSnapshotKey(bucket, activeVersion), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"4101"}, ids)
	ttl, err := cache.rdb.TTL(ctx, schedulerSnapshotKey(bucket, activeVersion)).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, time.Duration(snapshotGraceTTLSeconds)*time.Second)

	buckets, err := cache.ListBuckets(ctx)
	require.NoError(t, err)
	require.NotContains(t, buckets, bucket)
	require.Contains(t, buckets, otherBucket)
	otherSnapshot, otherHit, err := cache.GetSnapshot(ctx, otherBucket)
	require.NoError(t, err)
	require.True(t, otherHit)
	require.Len(t, otherSnapshot, 1)
	require.Equal(t, otherProvider.ID, otherSnapshot[0].ID)
	otherEpochAfter, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerEpochPrefix, otherBucket)).Int64()
	require.NoError(t, err)
	require.Equal(t, otherEpoch, otherEpochAfter)

	_, err = cache.CaptureBucketWriteToken(ctx, bucket)
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketRetired)
	versionBeforeRejectedWrite, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerVersionPrefix, bucket)).Int64()
	require.NoError(t, err)
	err = cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider})
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketRetired)
	versionAfterRejectedWrite, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerVersionPrefix, bucket)).Int64()
	require.NoError(t, err)
	require.Equal(t, versionBeforeRejectedWrite, versionAfterRejectedWrite, "fenced writers must not allocate a new version")
	retired, err := cache.rdb.Exists(ctx, schedulerBucketKey(schedulerRetiredPrefix, bucket)).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, retired, "ordinary writers must never clear the tombstone")
	// Advance beyond retirement's grace window after checking new-reader misses,
	// then verify the previously retained membership key is removed by fixture
	// expiry.
	mr.FastForward(time.Duration(snapshotGraceTTLSeconds+1) * time.Second)
	exists, err := cache.rdb.Exists(ctx, schedulerSnapshotKey(bucket, activeVersion)).Result()
	require.NoError(t, err)
	require.Zero(t, exists, "retired active snapshot must expire after the in-flight grace period")

	newToken, err := cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.True(t, newToken.ValidFor(bucket))
	require.Equal(t, retiredEpoch, newToken.Epoch)
	reopenedAgain, err := cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.Equal(t, newToken, reopenedAgain, "reopen must be idempotent within one retirement generation")
	err = cache.SetSnapshot(ctx, bucket, token, []providercore.Record{provider})
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketWriteFenced)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, newToken, []providercore.Record{provider}))
	reopenedWhileOpen, err := cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.Equal(t, newToken, reopenedWhileOpen)

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, provider.ID, snapshot[0].ID)
}

// TestSchedulerCacheActivationIsFencedAfterRetire retires and reopens between
// an old writer's preparation and activation. It checks the old activation
// fails, removes its unpublished membership and leaves no
// active/ready/registration markers.
func TestSchedulerCacheActivationIsFencedAfterRetire(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := scheduler.SchedulerBucket{GroupID: 51, Platform: capability.PlatformAnthropic, Mode: scheduler.SchedulerModeMixed}
	provider := providercore.Record{ID: 5101, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey}

	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	version, err := cache.allocateSnapshotVersion(ctx, bucket, token)
	require.NoError(t, err)
	_, err = cache.writeSnapshotVersionAndReturnProviderIDs(ctx, bucket, version, []providercore.Record{provider})
	require.NoError(t, err)

	// Change the generation after payload and membership preparation so
	// activation must recheck the token even though this writer already finished
	// its earlier writes.
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	_, err = cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	err = cache.activateSnapshotVersion(ctx, bucket, token, version)
	require.ErrorIs(t, err, scheduler.ErrSchedulerBucketWriteFenced)

	exists, err := cache.rdb.Exists(ctx, schedulerSnapshotKey(bucket, version)).Result()
	require.NoError(t, err)
	require.Zero(t, exists, "fenced activation must delete its unpublished snapshot")
	exists, err = cache.rdb.Exists(
		ctx,
		schedulerBucketKey(schedulerReadyPrefix, bucket),
		schedulerBucketKey(schedulerActivePrefix, bucket),
	).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
	buckets, err := cache.ListBuckets(ctx)
	require.NoError(t, err)
	require.NotContains(t, buckets, bucket)
}

// TestSchedulerCacheConcurrentReopenReturnsSameToken releases two goroutines
// from one barrier to reopen a retired bucket. It checks equal newer tokens,
// old-token rejection and successful publication with the returned
// generation.
func TestSchedulerCacheConcurrentReopenReturnsSameToken(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := scheduler.SchedulerBucket{GroupID: 53, Platform: capability.PlatformOpenAI, Mode: scheduler.SchedulerModeForced}
	provider := providercore.Record{ID: 5301, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey}

	oldToken, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.RetireBucket(ctx, bucket))

	// reopenResult carries one concurrent bucket reopen token and its operation
	// error.
	type reopenResult struct {
		// token and err carry the same ReopenBucket call through the result
		// channel; assertions inspect generation identity after requiring that
		// call's error to be nil.
		token scheduler.SchedulerBucketWriteToken // returned bucket generation token
		err   error                               // reopen operation failure cause
	}
	// Gate both reopen workers on one channel and buffer one result per worker,
	// then release the gate and collect both outcomes before comparing
	// generations.
	start := make(chan struct{})
	results := make(chan reopenResult, 2)
	for range 2 {
		go func() {
			<-start
			token, err := cache.ReopenBucket(ctx, bucket)
			results <- reopenResult{token: token, err: err}
		}()
	}
	close(start)
	first := <-results
	second := <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, first.token, second.token)
	require.Greater(t, first.token.Epoch, oldToken.Epoch)

	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, oldToken, []providercore.Record{provider}), scheduler.ErrSchedulerBucketWriteFenced)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, first.token, []providercore.Record{provider}))
}

// TestSchedulerCacheReopenExpiresPreviousActiveSnapshot seeds retirement
// markers while an old version remains active. It checks reopening removes
// readiness, expires the old members, fences the prior token and permits new
// publication.
func TestSchedulerCacheReopenExpiresPreviousActiveSnapshot(t *testing.T) {
	ctx := context.Background()
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	bucket := scheduler.SchedulerBucket{GroupID: 52, Platform: capability.PlatformGemini, Mode: scheduler.SchedulerModeForced}
	provider := providercore.Record{ID: 5201, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey}

	oldToken, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, oldToken, []providercore.Record{provider}))
	oldVersion, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerActivePrefix, bucket)).Result()
	require.NoError(t, err)
	retiredEpoch := oldToken.Epoch + 1
	require.NoError(t, cache.rdb.Set(ctx, schedulerBucketKey(schedulerEpochPrefix, bucket), retiredEpoch, 0).Err())
	require.NoError(t, cache.rdb.Set(ctx, schedulerBucketKey(schedulerRetiredPrefix, bucket), retiredEpoch, 0).Err())

	newToken, err := cache.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.Equal(t, retiredEpoch, newToken.Epoch)
	_, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
	ttl, err := cache.rdb.TTL(ctx, schedulerSnapshotKey(bucket, oldVersion)).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, time.Duration(snapshotGraceTTLSeconds)*time.Second)

	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, oldToken, []providercore.Record{provider}), scheduler.ErrSchedulerBucketWriteFenced)
	// Advance beyond the reopening grace window to remove the superseded
	// membership set before publishing a version under the returned generation.
	mr.FastForward(time.Duration(snapshotGraceTTLSeconds+1) * time.Second)
	exists, err := cache.rdb.Exists(ctx, schedulerSnapshotKey(bucket, oldVersion)).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, newToken, []providercore.Record{provider}))
}

// TestSchedulerCacheGroupLifecycleLeaseConcurrentAcquireSingleOwner releases
// thirty-two acquisition workers from one barrier. It checks one valid owner,
// empty loser leases, lowercase hexadecimal token shape and a different owner
// after release/reacquisition.
func TestSchedulerCacheGroupLifecycleLeaseConcurrentAcquireSingleOwner(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	const groupID int64 = 71

	// result carries one concurrent group-lock attempt's handle, confirmation
	// and failure.
	type result struct {
		// lease, acquired and err describe one acquisition attempt in the buffered
		// result channel; successful owners supply a valid lease while unsuccessful
		// confirmations keep the empty handle.
		lease    scheduler.SchedulerGroupLifecycleLease // returned group owner handle
		acquired bool                                   // confirmed lock acquisition flag
		err      error                                  // lease attempt failure cause
	}
	// Gate acquisition workers together and buffer every outcome so result sends
	// can finish before collection; inspect all attempts before releasing the
	// winning owner.
	start := make(chan struct{})
	results := make(chan result, 32)
	for range 32 {
		go func() {
			<-start
			lease, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
			results <- result{lease: lease, acquired: acquired, err: err}
		}()
	}
	close(start)

	var owner scheduler.SchedulerGroupLifecycleLease
	acquiredCount := 0
	for range 32 {
		got := <-results
		require.NoError(t, got.err)
		if got.acquired {
			acquiredCount++
			owner = got.lease
			require.True(t, got.lease.ValidFor(groupID))
		} else {
			require.Equal(t, scheduler.SchedulerGroupLifecycleLease{}, got.lease)
		}
	}
	require.Equal(t, 1, acquiredCount)
	require.Len(t, owner.OwnerToken, schedulerGroupLifecycleOwnerTokenBytes*2)
	require.Equal(t, strings.ToLower(owner.OwnerToken), owner.OwnerToken)
	decodedOwner, err := hex.DecodeString(owner.OwnerToken)
	require.NoError(t, err)
	require.Len(t, decodedOwner, schedulerGroupLifecycleOwnerTokenBytes)

	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, owner))
	next, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, next.ValidFor(groupID))
	require.NotEqual(t, owner.OwnerToken, next.OwnerToken)
	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, next))
}

// TestSchedulerCacheGroupLifecycleLeaseStaleReleaseCannotDeleteSuccessor
// expires one owner before acquiring a successor. It checks stale release
// loss, retained successor identity and lost ownership on a repeated
// successor release.
func TestSchedulerCacheGroupLifecycleLeaseStaleReleaseCannotDeleteSuccessor(t *testing.T) {
	ctx := context.Background()
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	const groupID int64 = 72
	const ttl = time.Minute

	first, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, ttl)
	require.NoError(t, err)
	require.True(t, acquired)

	// Expire the first owner's key before acquiring its successor so stale-token
	// release is tested against a new live owner stored for that group.
	mr.FastForward(ttl + time.Second)
	second, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, ttl)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotEqual(t, first.OwnerToken, second.OwnerToken)

	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, first), scheduler.ErrSchedulerGroupLifecycleLeaseLost)
	owner, err := cache.rdb.Get(ctx, schedulerGroupLifecycleLockKey(groupID)).Result()
	require.NoError(t, err)
	require.Equal(t, second.OwnerToken, owner)

	_, acquired, err = cache.TryAcquireGroupLifecycleLease(ctx, groupID, ttl)
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, second))
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, second), scheduler.ErrSchedulerGroupLifecycleLeaseLost)
}

// TestSchedulerCacheGroupLifecycleLeaseExpiredReleaseIsLost expires a group
// owner's fixture key before releasing it. It checks the release reports
// lease loss even when no successor has been acquired.
func TestSchedulerCacheGroupLifecycleLeaseExpiredReleaseIsLost(t *testing.T) {
	ctx := context.Background()
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	const groupID int64 = 73
	const ttl = time.Minute

	lease, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, groupID, ttl)
	require.NoError(t, err)
	require.True(t, acquired)
	// Expire the sole owner's key before release so a missing lock, with no
	// successor present, still produces the lease-loss result.
	mr.FastForward(ttl + time.Second)

	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, lease), scheduler.ErrSchedulerGroupLifecycleLeaseLost)
}

// TestSchedulerCacheGroupLifecycleLeaseWrongOwnerAndCrossGroupAreLost
// acquires separate group owners before using forged and cross-group handles.
// It checks both invalid releases report loss while each real owner remains
// stored and releasable.
func TestSchedulerCacheGroupLifecycleLeaseWrongOwnerAndCrossGroupAreLost(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	const firstGroupID int64 = 74
	const secondGroupID int64 = 75

	first, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, firstGroupID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	second, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, secondGroupID, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired, "different groups must acquire independently")
	require.NotEqual(t, first.OwnerToken, second.OwnerToken)

	// Choose a forged token distinct from the real random owner so the loss
	// assertion cannot accidentally exercise a valid owner release.
	wrongOwner := first
	wrongOwner.OwnerToken = strings.Repeat("0", schedulerGroupLifecycleOwnerTokenBytes*2)
	if wrongOwner.OwnerToken == first.OwnerToken {
		wrongOwner.OwnerToken = strings.Repeat("1", schedulerGroupLifecycleOwnerTokenBytes*2)
	}
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, wrongOwner), scheduler.ErrSchedulerGroupLifecycleLeaseLost)

	crossGroup := first
	crossGroup.GroupID = secondGroupID
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(ctx, crossGroup), scheduler.ErrSchedulerGroupLifecycleLeaseLost)

	firstOwner, err := cache.rdb.Get(ctx, schedulerGroupLifecycleLockKey(firstGroupID)).Result()
	require.NoError(t, err)
	require.Equal(t, first.OwnerToken, firstOwner)
	secondOwner, err := cache.rdb.Get(ctx, schedulerGroupLifecycleLockKey(secondGroupID)).Result()
	require.NoError(t, err)
	require.Equal(t, second.OwnerToken, secondOwner)

	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, first))
	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, second))
}

// TestSchedulerCacheGroupLifecycleLeaseCanceledContextFailsClosed cancels
// acquisition before later acquiring with a usable context. It checks
// rejected acquisition, failed canceled release, retained ownership and
// successful cleanup with the usable context.
func TestSchedulerCacheGroupLifecycleLeaseCanceledContextFailsClosed(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	canceledCtx, cancel := context.WithCancel(context.Background())
	// Cancel before attempting lock acquisition so the fixture checks context
	// failure without first creating an owner key that would require cleanup.
	cancel()

	lease, acquired, err := cache.TryAcquireGroupLifecycleLease(canceledCtx, 76, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, acquired)
	require.Equal(t, scheduler.SchedulerGroupLifecycleLease{}, lease)

	ctx := context.Background()
	lease, acquired, err = cache.TryAcquireGroupLifecycleLease(ctx, 76, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(canceledCtx, lease), context.Canceled)
	owner, err := cache.rdb.Get(ctx, schedulerGroupLifecycleLockKey(lease.GroupID)).Result()
	require.NoError(t, err)
	require.Equal(t, lease.OwnerToken, owner)
	require.NoError(t, cache.ReleaseGroupLifecycleLease(ctx, lease))
}

// TestSchedulerCacheGroupLifecycleLeaseRejectsInvalidInput supplies a zero
// group ID, zero TTL and invalid release handle with a canceled context. It
// checks lease-invalid errors, empty results and no fixture keys created.
func TestSchedulerCacheGroupLifecycleLeaseRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)

	lease, acquired, err := cache.TryAcquireGroupLifecycleLease(ctx, 0, time.Minute)
	require.ErrorIs(t, err, scheduler.ErrSchedulerGroupLifecycleLeaseInvalid)
	require.False(t, acquired)
	require.Equal(t, scheduler.SchedulerGroupLifecycleLease{}, lease)

	lease, acquired, err = cache.TryAcquireGroupLifecycleLease(ctx, 73, 0)
	require.ErrorIs(t, err, scheduler.ErrSchedulerGroupLifecycleLeaseInvalid)
	require.False(t, acquired)
	require.Equal(t, scheduler.SchedulerGroupLifecycleLease{}, lease)

	canceledCtx, cancel := context.WithCancel(ctx)
	// Use a canceled context with an invalid handle to check that lease
	// validation precedes Redis dispatch and returns the invalid-input error.
	cancel()
	require.ErrorIs(t, cache.ReleaseGroupLifecycleLease(canceledCtx, scheduler.SchedulerGroupLifecycleLease{}), scheduler.ErrSchedulerGroupLifecycleLeaseInvalid)
	keys, err := cache.rdb.DBSize(ctx).Result()
	require.NoError(t, err)
	require.Zero(t, keys)
}

// BenchmarkSchedulerCacheProviderPayloadReuse compares repeated encoding with
// reusable IDs for fixed provider counts and one/two-bucket workloads. It
// records allocations and local aggregate work, including member entries,
// without sending Redis publication commands.
func BenchmarkSchedulerCacheProviderPayloadReuse(b *testing.B) {
	for _, size := range []int{1, 100, 10_000} {
		providers := schedulerCacheBenchmarkProviders(size)
		b.Run(fmt.Sprintf("pair_baseline_%d_providers", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				first, err := benchmarkSchedulerLegacySnapshotPayload(providers)
				if err != nil {
					b.Fatal(err)
				}
				second, err := benchmarkSchedulerLegacySnapshotPayload(providers)
				if err != nil {
					b.Fatal(err)
				}
				schedulerCachePayloadBenchmarkSink = first + second
			}
		})
		b.Run(fmt.Sprintf("pair_reuse_%d_providers", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				ids, total, err := benchmarkSchedulerReusableSnapshotPayload(providers)
				if err != nil {
					b.Fatal(err)
				}
				// Count later bucket-member construction from the retained IDs without
				// repeating encoding; this models the local reusable-ID workload and
				// performs no Redis writes.
				total += len(schedulerSnapshotMembers(ids))
				schedulerCachePayloadBenchmarkSink = total
			}
		})
		b.Run(fmt.Sprintf("first_baseline_%d_providers", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				total, err := benchmarkSchedulerLegacySnapshotPayload(providers)
				if err != nil {
					b.Fatal(err)
				}
				schedulerCachePayloadBenchmarkSink = total
			}
		})
		b.Run(fmt.Sprintf("first_reuse_%d_providers", size), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				ids, total, err := benchmarkSchedulerReusableSnapshotPayload(providers)
				if err != nil {
					b.Fatal(err)
				}
				total += len(ids)
				schedulerCachePayloadBenchmarkSink = total
			}
		})
	}
}

// benchmarkSchedulerLegacySnapshotPayload sums full/metadata byte lengths and
// builds ordinal members for successfully encoded provider records. Encoding
// errors are skipped, and the returned error is nil for this local workload.
//
// A temporary record slice keeps the member inputs aligned with the providers
// whose payloads were counted. Its copied Record values retain nested
// references from the stable benchmark fixtures. The returned integer adds
// member-entry count to payload bytes, so it is an aggregate sink value
// rather than a serialized network byte size; no Redis publication occurs
// here.
func benchmarkSchedulerLegacySnapshotPayload(providers []providercore.Record) (int, error) {
	cacheable := make([]providercore.Record, 0, len(providers))
	total := 0
	for _, provider := range providers {
		full, meta, err := marshalSchedulerCacheProvider(provider)
		// Omit failed encodings from both byte accounting and later member
		// construction so the local comparison counts the same retained provider
		// population.
		if err != nil {
			continue
		}
		total += len(full) + len(meta)
		cacheable = append(cacheable, provider)
	}
	// Assign ordinals from the retained provider slice so skipped encodings do
	// not consume member positions or separate membership from the counted
	// payload population.
	members := make([]redis.Z, 0, len(cacheable))
	for idx, provider := range cacheable {
		members = append(members, redis.Z{Score: float64(idx), Member: strconv.FormatInt(provider.ID, 10)})
	}
	return total + len(members), nil
}

// benchmarkSchedulerReusableSnapshotPayload returns successful provider IDs
// and the full/metadata byte total plus first-bucket member count. Encoding
// failures are skipped, the ID list keeps input order and duplicates, and the
// returned error is nil.
//
// Members are constructed from retained IDs so a caller can perform later
// bucket-member work without encoding the provider data again. The aggregate
// combines bytes with entry counts to keep the benchmark result observable.
// It describes local construction work on stable fixtures, leaving Redis
// writes, version allocation and activation outside this helper.
func benchmarkSchedulerReusableSnapshotPayload(providers []providercore.Record) ([]int64, int, error) {
	providerIDs := make([]int64, 0, len(providers))
	total := 0
	for _, provider := range providers {
		full, meta, err := marshalSchedulerCacheProvider(provider)
		// Keep successful encoded IDs aligned with counted payload bytes by
		// omitting failures from both outputs before first-bucket members are
		// constructed.
		if err != nil {
			continue
		}
		total += len(full) + len(meta)
		providerIDs = append(providerIDs, provider.ID)
	}
	total += len(schedulerSnapshotMembers(providerIDs))
	return providerIDs, total, nil
}

// schedulerCacheBenchmarkProviders constructs size records with distinct IDs
// and shared credential/extra maps for read-only payload benchmarks. Supply a
// nonnegative size; callers keep the shared nested values stable during
// encoding.
func schedulerCacheBenchmarkProviders(size int) []providercore.Record {
	// Use sizeable arbitrary fields to exercise full-versus-filtered payload
	// encoding.
	largeValue := strings.Repeat("x", 4096)
	credentials := map[string]any{
		"api_key":       "benchmark-key",
		"model_mapping": map[string]any{"z-model": "z-target", "a-model": "a-target"},
		"large_value":   largeValue,
	}
	extra := map[string]any{
		"mixed_scheduling": true,
		"model_rate_limits": map[string]any{
			"z-model": map[string]any{"rate_limit_reset_at": "2026-07-16T00:00:00Z"},
			"a-model": map[string]any{"rate_limit_reset_at": "2026-07-16T00:00:00Z"},
		},
		"large_value": largeValue,
	}
	providers := make([]providercore.Record, size)
	for i := range providers {
		id := int64(i + 1)
		providers[i] = providercore.Record{
			ID:          id,
			Name:        "benchmark-provider",
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Credentials: credentials,
			Extra:       extra,
			GroupIDs:    []int64{7, 9},
			ProviderGroups: []providercore.GroupMembership{
				{ProviderID: id, GroupID: 7},
				{ProviderID: id, GroupID: 9},
			},
		}
	}
	return providers
}

// SetSnapshotAndReturnProviderIDs wraps fixture records and delegates
// token-checked preparation/publication to the core cache. Inputs stay stable
// during encoding; returned IDs include encodable duplicates in their
// supplied order.
func (c *schedulerCache) SetSnapshotAndReturnProviderIDs(ctx context.Context, bucket scheduler.SchedulerBucket, token scheduler.SchedulerBucketWriteToken, values []providercore.Record) ([]int64, error) {
	return c.SnapshotCache.SetSnapshotAndReturnProviderIDs(ctx, bucket, token, snapshotRecords(values))
}

// newSchedulerCacheWithChunkSizes constructs a provider-codec fixture with
// read/write batch overrides. Positive sizes select the requested limits,
// while nonpositive values use core defaults; the caller retains Redis client
// ownership.
func newSchedulerCacheWithChunkSizes(rdb *redis.Client, read, write int) *schedulerCache {
	return &schedulerCache{NewSnapshotCache(rdb, codec.ProviderCodec{}, SnapshotCacheOptions{MGetChunkSize: read, WriteChunkSize: write})}
}

// writeProviderIDs wraps fixture records and delegates shared full/metadata
// writes to the core cache. Encoding failures can omit IDs, while Redis
// errors can follow earlier completed writes.
func (c *schedulerCache) writeProviderIDs(ctx context.Context, values []providercore.Record) ([]int64, error) {
	return c.SnapshotCache.writeProviderIDs(ctx, snapshotRecords(values))
}

// writeSnapshotVersionAndReturnProviderIDs wraps fixture records for shared
// payload and unpublished membership preparation. The supplied version comes
// from allocation, and failure can leave earlier provider writes completed
// before activation is attempted.
func (c *schedulerCache) writeSnapshotVersionAndReturnProviderIDs(ctx context.Context, bucket scheduler.SchedulerBucket, version string, values []providercore.Record) ([]int64, error) {
	return c.SnapshotCache.writeSnapshotVersionAndReturnProviderIDs(ctx, bucket, version, snapshotRecords(values))
}
