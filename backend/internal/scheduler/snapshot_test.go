package scheduler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

var batchQueryBenchmarkProviderCount int

type batchProviderQueryKey struct {
	groupID  int64
	platform string
	mixed    bool
}

type batchProviderQueryResult struct {
	providers []SnapshotProvider
	err       error
}

type batchProviderQueryRepo struct {
	SnapshotProviderSource

	mu        sync.Mutex
	calls     map[batchProviderQueryKey]int
	results   map[batchProviderQueryKey][]batchProviderQueryResult
	beforeRun func(batchProviderQueryKey)
}

type batchSnapshotWrite struct {
	token     SchedulerBucketWriteToken
	providers []SnapshotProvider
}

type batchSnapshotCache struct {
	SnapshotCache

	mu          sync.Mutex
	nextEpoch   int64
	captures    []SchedulerBucket
	captured    map[SchedulerBucket]SchedulerBucketWriteToken
	locks       map[SchedulerBucket]int
	lockBusy    map[SchedulerBucket]bool
	lockErrors  map[SchedulerBucket]error
	setErrors   map[SchedulerBucket]error
	setAttempts map[SchedulerBucket]int
	writes      map[SchedulerBucket][]batchSnapshotWrite
	versions    map[SchedulerBucket]int
	beforeSet   func()
}

type batchSnapshotProviderIDCache struct {
	*batchSnapshotCache

	reuseMu     sync.Mutex
	fullCalls   map[SchedulerBucket]int
	idOnlyCalls map[SchedulerBucket]int
	idOnlyError map[SchedulerBucket]error
	fullLateErr map[SchedulerBucket]error
	returnEmpty bool
}

type batchQueryBenchmarkRepo struct {
	SnapshotProviderSource
	providers []SnapshotProvider
}

type batchQueryBenchmarkCache struct {
	SnapshotCache
}

type bulkEventProviderRepo struct {
	*batchProviderQueryRepo
	providers []SnapshotProvider
}

type bulkEventSnapshotCache struct {
	*batchSnapshotCache

	providerMu        sync.Mutex
	setProviderIDs    []int64
	deleteProviderIDs []int64
}

type fullRebuildLifecycleCache struct {
	*groupLifecycleTestCache

	mu              sync.Mutex
	captureAttempts []SchedulerBucket
	captureErrors   map[string]error
	lockBusyOnce    map[string]bool
	watermark       int64
	watermarkWrites []int64
}

type fullRebuildLifecycleGroupRepo struct {
	SnapshotGroupSource

	mu              sync.Mutex
	activeIDs       []int64
	activeIDsErr    error
	listActiveErr   error
	fresh           map[int64]*SnapshotGroup
	freshErr        map[int64]error
	activeIDCalls   int
	listActiveCalls int
	freshCalls      []int64
}

type fullRebuildFallbackGroupRepo struct {
	SnapshotGroupSource

	mu        sync.Mutex
	groups    []SnapshotGroup
	err       error
	listCalls int
}

type fullRebuildProviderCall struct {
	groupID  int64
	platform string
}

type fullRebuildProviderRepo struct {
	SnapshotProviderSource

	mu          sync.Mutex
	calls       []fullRebuildProviderCall
	beforeFirst func()
	once        sync.Once
}

type schedulerFullRebuildTestCache struct {
	SnapshotCache

	mu        sync.Mutex
	listErr   error
	listCalls int
	captures  int
	lockCalls int
}

type groupLifecycleTestCache struct {
	*retirementRaceCache

	stateMu sync.Mutex

	leaseHeld       bool
	lease           SchedulerGroupLifecycleLease
	leaseSequence   int
	leaseBusy       bool
	leaseAcquireErr error
	leaseReleaseErr error
	acquireCalls    int
	releaseCalls    int
	acquireTTL      time.Duration
	acquireDeadline bool
	releaseDeadline bool
	releaseCtxErr   error

	listErr   error
	listCalls int

	retireCalls  []SchedulerBucket
	reopenTokens []SchedulerBucketWriteToken
	retireHeld   []bool
	reopenHeld   []bool
	retireErr    error
	retireErrAt  int
	reopenErr    error
	reopenErrAt  int

	bucketLockBusy bool
	bucketLockErr  error
	bucketLockTTLs []time.Duration
	unlockCalls    int
	setErr         error
}

type groupLifecycleTestGroupRepo struct {
	SnapshotGroupSource

	mu       sync.Mutex
	group    *SnapshotGroup
	err      error
	calls    int
	afterGet func()
}

type groupLifecycleTestProviderRepo struct {
	SnapshotProviderSource

	mu              sync.Mutex
	calls           int
	callsByPlatform map[string]int
	err             error
	started         chan struct{}
	release         chan struct{}
	once            sync.Once
	beforeLoad      func()
	beforeLoadOnce  sync.Once
}

type outboxCleanupCache struct {
	watermark       int64
	setWatermarks   []int64
	updateErr       error
	listBucketErr   error
	listBuckets     []SchedulerBucket
	listBucketCalls int
}

type outboxCleanupDeleteCall struct {
	watermark int64
	limit     int
}

type outboxCleanupRepo struct {
	events              []SchedulerOutboxEvent
	rows                []int64
	maxIDCalls          int
	maxIDErr            error
	lockAcquired        bool
	lockAttempts        int
	releaseCount        int
	deleteCalls         []outboxCleanupDeleteCall
	firstCreatedAfterID []int64
}

type outboxCleanupProviderRepo struct {
	SnapshotProviderSource
}

type blockingOutboxCleanupCache struct {
	*outboxCleanupCache
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

type outboxCleanupLease struct {
	release func()
}

type retirementRaceCache struct {
	SnapshotCache

	mu          sync.Mutex
	epochs      map[string]int64
	retired     map[string]bool
	listBuckets []SchedulerBucket
	captures    []SchedulerBucket
	reopens     []SchedulerBucket
	setAttempts map[string]int
	published   map[string]int
	versions    map[string]int
	beforeSet   func()
}

type retirementGroupRepo struct {
	SnapshotGroupSource
	groups []SnapshotGroup
	err    error
}

type schedulerSnapshotContextCacheStub struct {
	SnapshotCache
}

type schedulerSnapshotFallbackRepoStub struct {
	SnapshotProviderSource
	calls int
}

type snapshotTestProvider struct {
	ID          int64
	Name        string
	Platform    string
	Status      string
	Schedulable bool
	GroupIDs    []int64
}

// retirementProviderSource filters fixtures by platform and can block database queries at a test
// barrier.
type retirementProviderSource struct {
	SnapshotProviderSource
	providers        []SnapshotProvider
	listPlatformFunc func(context.Context, string) ([]SnapshotProvider, error)
}

// planSnapshotCache records and blocks rebuild calls for shutdown tests.
type planSnapshotCache struct {
	SnapshotCache
	calls chan struct{}
	block chan struct{}
}

// updateSnapshotValue provides rebuild metadata for cache update tests.
type updateSnapshotValue struct{ id int64 }

type updateSnapshotCache struct {
	SnapshotCache
	values []SnapshotProvider
	err    error
}

func TestSchedulerRebuildBatchReusesSingleForcedQueryAndKeepsSnapshotsIndependent(t *testing.T) {
	const groupID int64 = 201
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	wantCaptures := 2
	repo.beforeRun = func(batchProviderQueryKey) {
		require.Equal(t, wantCaptures, cache.captureCount(), "all tokens must be prepared before the first DB query")
		wantCaptures += 2
	}
	svc := newBatchQueryTestService(cache, repo)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "first"))
	queryKey := batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}
	require.Equal(t, 1, repo.callCount(queryKey))
	for _, bucket := range []SchedulerBucket{single, forced} {
		locks, attempts, version, writes := cache.bucketState(bucket)
		require.Equal(t, 1, locks, bucket.String())
		require.Equal(t, 1, attempts, bucket.String())
		require.Equal(t, 1, version, bucket.String())
		require.Len(t, writes, 1, bucket.String())
		require.Equal(t, "source", snapshotTestData(writes[0].providers[0]).Name, bucket.String())
		require.Equal(t, bucket, writes[0].token.Bucket)
	}
	_, _, _, singleWrites := cache.bucketState(single)
	_, _, _, forcedWrites := cache.bucketState(forced)
	require.NotEqual(t, singleWrites[0].token.Epoch, forcedWrites[0].token.Epoch)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "second"))
	require.Equal(t, 2, repo.callCount(queryKey), "successful results must not be cached across rebuild batches")
	for _, bucket := range []SchedulerBucket{single, forced} {
		locks, attempts, version, writes := cache.bucketState(bucket)
		require.Equal(t, 2, locks, bucket.String())
		require.Equal(t, 2, attempts, bucket.String())
		require.Equal(t, 2, version, bucket.String())
		require.Len(t, writes, 2, bucket.String())
		require.Equal(t, "source", snapshotTestData(writes[1].providers[0]).Name, bucket.String())
	}
}

func TestSchedulerRebuildBatchReusesProviderPayloadForSingleForced(t *testing.T) {
	const groupID int64 = 211
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	for run := 1; run <= 2; run++ {
		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "reuse"))
		full, idOnly := cache.reuseCounts(single)
		require.Equal(t, run, full)
		require.Zero(t, idOnly)
		full, idOnly = cache.reuseCounts(forced)
		require.Zero(t, full)
		require.Equal(t, run, idOnly)
	}
	require.Equal(t, 2, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}), "提供商载荷不得跨重建批次复用")
}

func TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterFirstWriterFailure(t *testing.T) {
	const groupID int64 = 212
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("snapshot write failed")
	cache := newBatchSnapshotProviderIDCache()
	cache.setErrors[single] = wantErr
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "failure")
	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(forced)
	require.Equal(t, 1, attempts, "首次完整写失败后，后续桶必须走原 SetSnapshot")
	require.Len(t, writes, 1)
}

func TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterLateFirstWriterFailure(t *testing.T) {
	const groupID int64 = 216
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("snapshot activation failed")
	cache := newBatchSnapshotProviderIDCache()
	cache.fullLateErr[single] = wantErr
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "late-failure")
	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(forced)
	require.Equal(t, 1, attempts, "首次激活失败后不得登记可复用 ID")
	require.Len(t, writes, 1)
}

func TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterLockBusy(t *testing.T) {
	const groupID int64 = 213
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	cache.lockBusy[single] = true
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "busy"))
	full, idOnly := cache.reuseCounts(single)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(forced)
	require.Equal(t, 1, attempts)
	require.Len(t, writes, 1)
}

func TestSchedulerRebuildBatchKeepsMixedAndDifferentQueriesOnFullWrites(t *testing.T) {
	const groupID int64 = 214
	openAISingle := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	openAIForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	anthropicSingle := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeSingle}
	anthropicMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	cache := newBatchSnapshotProviderIDCache()
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{openAISingle, openAIForced, anthropicSingle, anthropicMixed}, "scope"))
	full, idOnly := cache.reuseCounts(openAISingle)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(openAIForced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
	full, idOnly = cache.reuseCounts(anthropicSingle)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(anthropicSingle)
	require.Equal(t, 1, attempts)
	require.Len(t, writes, 1)
	full, idOnly = cache.reuseCounts(anthropicMixed)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, _ = cache.bucketState(anthropicMixed)
	require.Equal(t, 1, attempts, "mixed 桶必须继续走原 SetSnapshot")
}

func TestSchedulerRebuildBatchPropagatesProviderIDOnlyWriteFailure(t *testing.T) {
	const groupID int64 = 215
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("id-only write failed")
	cache := newBatchSnapshotProviderIDCache()
	cache.idOnlyError[forced] = wantErr
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "id-error")
	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(forced)
	require.Zero(t, full, "ID-only 失败不得静默回退为完整写")
	require.Equal(t, 1, idOnly)
}

func TestSchedulerRebuildBatchReusesSuccessfulEmptyProviderIDs(t *testing.T) {
	const groupID int64 = 217
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	cache.returnEmpty = true
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "empty"))
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly, "已成功缓存的空 ID 集也必须通过 map presence 复用")
	_, _, _, writes := cache.bucketState(forced)
	require.Len(t, writes, 1)
	require.Empty(t, writes[0].providers)
}

func TestSchedulerRebuildBatchReusesProviderPayloadForSimpleGroupZero(t *testing.T) {
	single := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
}

func TestSchedulerProviderQueryCacheReleasesSnapshotProviderIDs(t *testing.T) {
	single := schedulerBucketWriteTask{bucket: SchedulerBucket{GroupID: 218, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}
	forced := schedulerBucketWriteTask{bucket: SchedulerBucket{GroupID: 218, Platform: PlatformOpenAI, Mode: SchedulerModeForced}}
	queries := newSchedulerProviderQueryCache([]schedulerBucketWriteTask{single, forced})
	key, ok := schedulerProviderQueryKeyForBucket(single.bucket)
	require.True(t, ok)
	queries.snapshotProviderIDs[key] = []int64{1, 2}

	queries.release(single.bucket)
	require.Contains(t, queries.snapshotProviderIDs, key)
	queries.release(forced.bucket)
	require.NotContains(t, queries.snapshotProviderIDs, key)
	require.Empty(t, queries.remaining)
	require.Empty(t, queries.providers)
}

func TestSchedulerRebuildBatchKeepsMixedAndDifferentKeysIndependent(t *testing.T) {
	const groupID int64 = 202
	buckets := []SchedulerBucket{
		{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeForced},
		{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
		{GroupID: groupID + 1, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: groupID, Platform: PlatformGemini, Mode: SchedulerModeForced},
		{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: -1, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "test"))
	require.Equal(t, 2, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformAnthropic}))
	require.Zero(t, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformAnthropic, mixed: true}))
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID + 1, platform: PlatformAnthropic}))
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformGemini}))
	require.Zero(t, repo.callCount(batchProviderQueryKey{platform: PlatformOpenAI}), "没有有效分组时不查询提供商")
	for _, bucket := range buckets {
		locks, attempts, version, _ := cache.bucketState(bucket)
		require.Equal(t, 1, locks, bucket.String())
		require.Equal(t, 1, attempts, bucket.String())
		require.Equal(t, 1, version, bucket.String())
	}
}

func TestSchedulerRebuildBatchKeepsBucketGroupsIndependent(t *testing.T) {
	single := SchedulerBucket{GroupID: 204, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: 204, platform: PlatformOpenAI}))
	require.Zero(t, repo.callCount(batchProviderQueryKey{platform: PlatformOpenAI}))
}

func TestSchedulerRebuildBatchDoesNotCacheMixedOrHistoricalQueries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bucket SchedulerBucket
		key    batchProviderQueryKey
	}{
		{
			name:   "mixed",
			bucket: SchedulerBucket{GroupID: 204, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
			key:    batchProviderQueryKey{groupID: 204, platform: PlatformAnthropic},
		},
		{
			name:   "historical",
			bucket: SchedulerBucket{GroupID: 204, Platform: PlatformOpenAI, Mode: "unknown"},
			key:    batchProviderQueryKey{groupID: 204, platform: PlatformOpenAI},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newBatchSnapshotCache()
			token, err := cache.CaptureBucketWriteToken(context.Background(), tc.bucket)
			require.NoError(t, err)
			repo := newBatchProviderQueryRepo()
			svc := newBatchQueryTestService(cache, repo)
			tasks := []schedulerBucketWriteTask{
				{bucket: tc.bucket, token: token},
				{bucket: tc.bucket, token: token},
			}
			queries := newSchedulerProviderQueryCache(tasks)

			require.NoError(t, svc.rebuildPreparedBucketTasks(context.Background(), tasks, "test", false, queries))
			require.Equal(t, 2, repo.callCount(tc.key))
			require.Empty(t, queries.providers)
			locks, attempts, version, _ := cache.bucketState(tc.bucket)
			require.Equal(t, 2, locks)
			require.Equal(t, 2, attempts)
			require.Equal(t, 2, version)
		})
	}
}

func TestSchedulerRebuildBatchRetriesQueryFailureForFollowingBucket(t *testing.T) {
	const groupID int64 = 205
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("first query failed")
	key := batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}
	repo := newBatchProviderQueryRepo()
	repo.results[key] = []batchProviderQueryResult{
		{err: wantErr},
		{providers: []SnapshotProvider{snapshotTestProvider{ID: 2051, Name: "retry", Platform: PlatformOpenAI}}},
	}
	cache := newBatchSnapshotCache()
	svc := newBatchQueryTestService(cache, repo)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test")
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 2, repo.callCount(key), "failed queries must not enter the batch cache")
	_, singleAttempts, singleVersion, _ := cache.bucketState(single)
	_, forcedAttempts, forcedVersion, forcedWrites := cache.bucketState(forced)
	require.Zero(t, singleAttempts)
	require.Zero(t, singleVersion)
	require.Equal(t, 1, forcedAttempts)
	require.Equal(t, 1, forcedVersion)
	require.Equal(t, "retry", snapshotTestData(forcedWrites[0].providers[0]).Name)
}

func TestSchedulerFullRebuildSharesSuccessfulQueryAcrossStrictAndOrdinarySegments(t *testing.T) {
	const groupID int64 = 206
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotCache()
	cache.setErrors[single] = ErrSchedulerBucketWriteFenced
	singleToken, err := cache.CaptureBucketWriteToken(context.Background(), single)
	require.NoError(t, err)
	forcedToken, err := cache.CaptureBucketWriteToken(context.Background(), forced)
	require.NoError(t, err)
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	err = svc.prepareAndRebuildFullSnapshot(
		context.Background(),
		[]schedulerBucketWriteTask{{bucket: forced, token: forcedToken}},
		[]schedulerBucketWriteTask{{bucket: single, token: singleToken}},
		nil,
		"test",
	)
	require.ErrorIs(t, err, ErrSchedulerBucketWriteFenced)
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}), "SetSnapshot failure must not discard a successful query")
	_, singleAttempts, singleVersion, _ := cache.bucketState(single)
	_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
	require.Equal(t, 1, singleAttempts)
	require.Zero(t, singleVersion)
	require.Equal(t, 1, forcedAttempts)
	require.Equal(t, 1, forcedVersion)
}

func TestSchedulerRebuildBatchPreservesLockBusyAndFencingPolicy(t *testing.T) {
	const groupID int64 = 207
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}

	t.Run("ordinary lock busy skips only that bucket", func(t *testing.T) {
		cache := newBatchSnapshotCache()
		cache.lockBusy[single] = true
		repo := newBatchProviderQueryRepo()
		svc := newBatchQueryTestService(cache, repo)

		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
		require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}))
		_, singleAttempts, _, _ := cache.bucketState(single)
		_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
		require.Zero(t, singleAttempts)
		require.Equal(t, 1, forcedAttempts)
		require.Equal(t, 1, forcedVersion)
	})

	t.Run("strict lock busy is returned while ordinary work continues", func(t *testing.T) {
		cache := newBatchSnapshotCache()
		cache.lockBusy[single] = true
		singleToken, err := cache.CaptureBucketWriteToken(context.Background(), single)
		require.NoError(t, err)
		forcedToken, err := cache.CaptureBucketWriteToken(context.Background(), forced)
		require.NoError(t, err)
		repo := newBatchProviderQueryRepo()
		svc := newBatchQueryTestService(cache, repo)

		err = svc.prepareAndRebuildFullSnapshot(
			context.Background(),
			[]schedulerBucketWriteTask{{bucket: forced, token: forcedToken}},
			[]schedulerBucketWriteTask{{bucket: single, token: singleToken}},
			nil,
			"test",
		)
		require.ErrorIs(t, err, ErrSchedulerBucketRebuildBusy)
		require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}))
		_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
		require.Equal(t, 1, forcedAttempts)
		require.Equal(t, 1, forcedVersion)
	})

	t.Run("ordinary fencing stays non-fatal", func(t *testing.T) {
		cache := newBatchSnapshotCache()
		cache.setErrors[single] = ErrSchedulerBucketWriteFenced
		repo := newBatchProviderQueryRepo()
		svc := newBatchQueryTestService(cache, repo)

		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
		require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}))
		_, singleAttempts, singleVersion, _ := cache.bucketState(single)
		_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
		require.Equal(t, 1, singleAttempts)
		require.Zero(t, singleVersion)
		require.Equal(t, 1, forcedAttempts)
		require.Equal(t, 1, forcedVersion)
	})
}

func TestSchedulerRebuildBatchReleasesResultsAfterLastConsumer(t *testing.T) {
	const groups = 128
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	tasks := make([]schedulerBucketWriteTask, 0, groups*2)
	wantLockErr := errors.New("lock failed")
	for i := 1; i <= groups; i++ {
		groupID := int64(300 + i)
		single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
		forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
		if i == 1 {
			cache.lockBusy[single] = true
		}
		if i == 2 {
			cache.lockErrors[single] = wantLockErr
		}
		for _, bucket := range []SchedulerBucket{single, forced} {
			token, err := cache.CaptureBucketWriteToken(context.Background(), bucket)
			require.NoError(t, err)
			tasks = append(tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
		}
	}
	queries := newSchedulerProviderQueryCache(tasks)
	maxResident := 0
	cache.beforeSet = func() {
		if resident := len(queries.providers); resident > maxResident {
			maxResident = resident
		}
	}
	svc := newBatchQueryTestService(cache, repo)

	err := svc.rebuildPreparedBucketTasks(context.Background(), tasks, "test", false, queries)
	require.ErrorIs(t, err, wantLockErr)
	require.LessOrEqual(t, maxResident, 1, "adjacent single/forced pairs must not accumulate full-batch results")
	require.Empty(t, queries.providers)
	require.Empty(t, queries.remaining)
	for i := 1; i <= groups; i++ {
		key := batchProviderQueryKey{groupID: int64(300 + i), platform: PlatformOpenAI}
		require.Equal(t, 1, repo.callCount(key), key)
	}
}

func BenchmarkSchedulerRebuildBatchQueryReuse(b *testing.B) {
	const groupID int64 = 208
	buckets := []SchedulerBucket{
		{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}
	for _, tc := range []struct {
		name string
		size int
	}{
		{name: "1_provider", size: 1},
		{name: "10000_providers", size: 10_000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			providers := make([]SnapshotProvider, tc.size)
			svc := newBatchQueryTestService(
				&batchQueryBenchmarkCache{},
				&batchQueryBenchmarkRepo{providers: providers},
			)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := svc.rebuildBuckets(context.Background(), buckets, "benchmark"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSchedulerBulkProviderEventScopesOpenAIRebuildToFreshPlatform(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 1, Platform: PlatformOpenAI, GroupIDs: []int64{12}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{1}, []int64{11}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{11, 12}, PlatformOpenAI), cache.capturedBuckets())
	set, deleted := cache.providerWrites()
	require.Equal(t, []int64{1}, set)
	require.Empty(t, deleted)
}

// TestSchedulerBulkProviderEventScopesQoderRebuildToFreshPlatform checks Qoder and shared buckets in
// former and current groups.
func TestSchedulerBulkProviderEventScopesQoderRebuildToFreshPlatform(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 13, Platform: PlatformQoder, GroupIDs: []int64{82}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{13}, []int64{81}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{81, 82}, PlatformQoder), cache.capturedBuckets())
}

func TestSchedulerBulkProviderEventRebuildsOpenAIUngroupedBucket(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 6, Platform: PlatformOpenAI})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{6}, nil), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{0}, PlatformOpenAI), cache.capturedBuckets())
}

func TestSchedulerBulkProviderEventKeepsGroupedAndUngroupedBuckets(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(
		&snapshotTestProvider{ID: 7, Platform: PlatformOpenAI, GroupIDs: []int64{51}},
		&snapshotTestProvider{ID: 8, Platform: PlatformOpenAI},
	)
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{7, 8}, nil), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{0, 51}, PlatformOpenAI), cache.capturedBuckets())
}

func TestSchedulerBulkProviderEventDoesNotCrossCurrentGroupsBetweenPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(
		&snapshotTestProvider{ID: 9, Platform: PlatformOpenAI, GroupIDs: []int64{61}},
		&snapshotTestProvider{ID: 10, Platform: PlatformGrok, GroupIDs: []int64{62}},
	)
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{9, 10}, []int64{63}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	want := append(
		schedulerBucketsForTest([]int64{61, 63}, PlatformOpenAI),
		schedulerBucketsForTest([]int64{62, 63}, PlatformGrok)...,
	)
	require.ElementsMatch(t, dedupeBuckets(want), cache.capturedBuckets())
}

func TestSchedulerBulkProviderEventKeepsGroupMembershipIn(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 11, Platform: PlatformOpenAI, GroupIDs: []int64{71}})
	svc := NewSnapshotService(cache, nil, repo, nil, &SnapshotOptions{})

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{11}, []int64{72}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{71, 72}, PlatformOpenAI), cache.capturedBuckets())
}

func TestSchedulerBulkProviderEventRefreshesAntigravityAndSharedPool(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	// Antigravity updates also refresh the groups' shared provider pools.
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 2, Platform: PlatformAntigravity, GroupIDs: []int64{22}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{2}, []int64{21}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t,
		schedulerBucketsForTest([]int64{21, 22}, PlatformAntigravity),
		cache.capturedBuckets(),
	)
}

func TestSchedulerBulkProviderEventMissingProviderFallsBackToAllPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 3, Platform: PlatformOpenAI, GroupIDs: []int64{32}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{3, 4}, []int64{31}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	platforms := schedulerSnapshotPlatforms()
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{31, 32}, platforms...), cache.capturedBuckets())
	set, deleted := cache.providerWrites()
	require.Equal(t, []int64{3}, set)
	require.Equal(t, []int64{4}, deleted)
}

func TestSchedulerBulkProviderEventUnknownPlatformFallsBackToAllPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 5, GroupIDs: []int64{42}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{5}, []int64{41}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	platforms := schedulerSnapshotPlatforms()
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{41, 42}, platforms...), cache.capturedBuckets())
}

func TestSchedulerFullRebuildActiveTombstoneDoesNotBlockFollowingGroupEvent(t *testing.T) {
	const groupID int64 = 101
	canonical := schedulerBucketsForGroup(groupID)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	outbox := &outboxCleanupRepo{events: []SchedulerOutboxEvent{
		{ID: 1, EventType: SchedulerOutboxEventFullRebuild},
		{ID: 2, EventType: SchedulerOutboxEventGroupChanged, GroupID: ptrInt64(groupID)},
	}}
	svc := newFullRebuildLifecycleService(cache, outbox, providers, groups)

	svc.pollOutbox()

	require.Equal(t, int64(2), cache.currentWatermark())
	cache.mu.Lock()
	require.Equal(t, []int64{2}, cache.watermarkWrites)
	cache.mu.Unlock()
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID, groupID}, freshCalls)
	require.Len(t, cache.tokens(), len(canonical)*2, "full rebuild and the following group event must each run fresh authority")
	_, reopenHeld := cache.lifecycleMutationLeaseStates()
	require.Len(t, reopenHeld, len(canonical)*2)
	for _, held := range reopenHeld {
		require.True(t, held)
	}
	require.Equal(t, expectedCanonicalProviderQueryCount()*2, providers.callCount())
}

func TestSchedulerFullRebuildGlobalReadErrorsFailBeforeMutationOrDB(t *testing.T) {
	t.Run("list buckets", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		cache.listErr = errors.New("registry failed")
		groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
		providers := &fullRebuildProviderRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, cache.listErr)
		activeCalls, fallbackCalls, freshCalls := groups.stats()
		require.Zero(t, activeCalls)
		require.Zero(t, fallbackCalls)
		require.Empty(t, freshCalls)
		requireFullRebuildNoMutationOrDB(t, cache, providers)
	})

	t.Run("list active ids without fallback", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		groups := &fullRebuildLifecycleGroupRepo{
			activeIDsErr:  errors.New("active ids failed"),
			listActiveErr: errors.New("fallback must not run"),
			fresh:         make(map[int64]*SnapshotGroup),
			freshErr:      make(map[int64]error),
		}
		providers := &fullRebuildProviderRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, groups.activeIDsErr)
		activeCalls, fallbackCalls, freshCalls := groups.stats()
		require.Equal(t, 1, activeCalls)
		require.Zero(t, fallbackCalls)
		require.Empty(t, freshCalls)
		requireFullRebuildNoMutationOrDB(t, cache, providers)
	})

	t.Run("list active fallback", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		groups := &fullRebuildFallbackGroupRepo{err: errors.New("active groups failed")}
		providers := &fullRebuildProviderRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, groups.err)
		groups.mu.Lock()
		require.Equal(t, 1, groups.listCalls)
		groups.mu.Unlock()
		requireFullRebuildNoMutationOrDB(t, cache, providers)
	})
}

func TestSchedulerFullRebuildFreshActivePreparesEveryTokenBeforeFirstDB(t *testing.T) {
	const groupID int64 = 102
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(historical)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	var capturesAtFirstDB int
	providers.beforeFirst = func() {
		capturesAtFirstDB = cache.captureAttemptCount()
		held, reopenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held)
		require.Equal(t, len(schedulerBucketsForGroup(groupID)), reopenCount)
		require.Equal(t, len(schedulerCanonicalBuckets(0))+1, capturesAtFirstDB, "C(0) and the historical bucket must be captured before DB")
	}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, capturesAtFirstDB, cache.captureAttemptCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+1, providers.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+1, providers.groupCallCount(groupID))
	_, historicalPublished := cache.counts(historical)
	require.Equal(t, 1, historicalPublished)
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID}, freshCalls)
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, listCalls, "known Rg must prevent a second registry read")
}

func TestSchedulerFullRebuildOrdinaryCaptureErrorReturnsBeforeFirstDB(t *testing.T) {
	first := SchedulerBucket{GroupID: 0, Platform: "legacy-a", Mode: "unknown"}
	last := SchedulerBucket{GroupID: 0, Platform: "legacy-b", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(first, last)
	wantErr := errors.New("capture failed")
	cache.captureErrors[last.String()] = wantErr
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	err := svc.rebuildFullSnapshot(context.Background(), "test")
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, len(schedulerCanonicalBuckets(0))+2, cache.captureAttemptCount(), "all canonical and ordinary captures must be attempted before returning")
	require.Zero(t, providers.callCount())
	require.Zero(t, cache.totalSetAttempts())
}

func TestSchedulerFullRebuildPreservesGroupZeroActiveHistoricalAndInvalidRegistryBuckets(t *testing.T) {
	const groupID int64 = 103
	groupZeroHistorical := SchedulerBucket{GroupID: 0, Platform: "legacy-zero", Mode: "unknown"}
	activeHistorical := SchedulerBucket{GroupID: groupID, Platform: "legacy-active", Mode: "unknown"}
	activeForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	activeMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	invalidHistorical := SchedulerBucket{GroupID: -7, Platform: "legacy-invalid", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(groupZeroHistorical, activeHistorical, activeForced, activeMixed, invalidHistorical)
	groups := &fullRebuildFallbackGroupRepo{groups: []SnapshotGroup{{ID: groupID, Status: StatusActive}}}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, len(schedulerCanonicalBuckets(0))*2+4, cache.captureAttemptCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+2, providers.callCount())
	groups.mu.Lock()
	require.Equal(t, 1, groups.listCalls)
	groups.mu.Unlock()
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	for _, bucket := range []SchedulerBucket{groupZeroHistorical, activeHistorical, activeForced, activeMixed, invalidHistorical} {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

func TestSchedulerFullRebuildActiveTombstoneFreshInactiveOrMissingFiltersAllGroupTasks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *SnapshotGroup
	}{
		{name: "inactive", group: &SnapshotGroup{ID: 104, Status: StatusDisabled, Hydrated: true}},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const groupID int64 = 104
			canonical := schedulerBucketsForGroup(groupID)
			historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
			cache := newFullRebuildLifecycleCache(historical)
			require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[5]))
			groups := &fullRebuildLifecycleGroupRepo{
				activeIDs: []int64{groupID},
				fresh:     make(map[int64]*SnapshotGroup),
				freshErr:  make(map[int64]error),
			}
			if tc.group != nil {
				groups.fresh[groupID] = tc.group
			}
			providers := &fullRebuildProviderRepo{}
			svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

			require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
			require.Zero(t, providers.groupCallCount(groupID))
			require.Zero(t, providers.groupCallCount(0))
			require.Empty(t, cache.tokens())
			require.Equal(t, bucketStrings(append(canonical, historical)), bucketStrings(cache.retiredBuckets()))
			for _, bucket := range append(canonical, historical) {
				attempts, published := cache.counts(bucket)
				require.Zero(t, attempts, bucket.String())
				require.Zero(t, published, bucket.String())
			}
			_, _, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, listCalls)
		})
	}
}

func TestSchedulerFullRebuildStaleCandidatesAreSortedAndNeverReopenedAcrossRounds(t *testing.T) {
	historical := []SchedulerBucket{
		{GroupID: 3, Platform: "legacy", Mode: "unknown"},
		{GroupID: 1, Platform: "legacy", Mode: "unknown"},
		{GroupID: 2, Platform: "legacy", Mode: "unknown"},
	}
	cache := newFullRebuildLifecycleCache(historical...)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*SnapshotGroup{
			1: {ID: 1, Status: StatusDisabled, Hydrated: true},
			3: {ID: 3, Status: StatusDisabled, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "first"))
	retireCallsAfterFirst := len(cache.retiredBuckets())
	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "second"))
	_, _, freshCalls := groups.stats()
	require.Equal(t, []int64{1, 2, 3}, freshCalls)
	require.Equal(t, retireCallsAfterFirst, len(cache.retiredBuckets()))
	require.Empty(t, cache.tokens())
	require.Zero(t, providers.groupCallCount(1))
	require.Zero(t, providers.groupCallCount(2))
	require.Zero(t, providers.groupCallCount(3))
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 2, listCalls, "each round must use only its one global registry snapshot")
	for _, bucket := range historical {
		require.Contains(t, bucketStrings(cache.retiredBuckets()), bucket.String())
	}
}

func TestSchedulerFullRebuildPartialLifecycleFailureReturnsBeforeDBAndRetries(t *testing.T) {
	historical := []SchedulerBucket{
		{GroupID: 1, Platform: "legacy", Mode: "unknown"},
		{GroupID: 2, Platform: "legacy", Mode: "unknown"},
		{GroupID: 3, Platform: "legacy", Mode: "unknown"},
	}
	wantErr := errors.New("fresh group query failed")
	cache := newFullRebuildLifecycleCache(historical...)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*SnapshotGroup{
			1: {ID: 1, Status: StatusDisabled, Hydrated: true},
			3: {ID: 3, Status: StatusDisabled, Hydrated: true},
		},
		freshErr: map[int64]error{2: wantErr},
	}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	err := svc.triggerFullRebuild("first")
	require.ErrorIs(t, err, wantErr)
	_, _, freshCalls := groups.stats()
	require.Equal(t, []int64{1, 2}, freshCalls)
	require.Zero(t, providers.callCount())
	require.Zero(t, cache.totalSetAttempts())
	retiredPerGroup := len(schedulerBucketsForGroup(1)) + 1
	require.Equal(t, retiredPerGroup, len(cache.retiredBuckets()))

	groups.mu.Lock()
	delete(groups.freshErr, 2)
	groups.fresh[2] = &SnapshotGroup{ID: 2, Status: StatusDisabled, Hydrated: true}
	groups.mu.Unlock()
	require.NoError(t, svc.triggerFullRebuild("retry"))
	_, _, freshCalls = groups.stats()
	require.Equal(t, []int64{1, 2, 2, 3}, freshCalls)
	require.Equal(t, retiredPerGroup*3, len(cache.retiredBuckets()))
	require.Zero(t, providers.callCount())
	require.Empty(t, cache.tokens())
}

func TestSchedulerFullRebuildActiveTombstoneLazyRecoveryDiscardsPartialCaptureTasks(t *testing.T) {
	const groupID int64 = 105
	canonical := schedulerBucketsForGroup(groupID)
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(canonical[0], canonical[4], historical)
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[5]))
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	var capturesAtFirstDB int
	providers.beforeFirst = func() {
		capturesAtFirstDB = cache.captureAttemptCount()
		held, reopenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held)
		require.Equal(t, len(canonical), reopenCount)
		require.Equal(t, len(schedulerCanonicalBuckets(0))+7, capturesAtFirstDB)
	}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, capturesAtFirstDB, cache.captureAttemptCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+1, providers.callCount())
	for _, bucket := range canonical {
		attempts, published := cache.counts(bucket)
		require.Equal(t, 1, attempts, "discarded pre-recovery tokens must never publish: %s", bucket.String())
		require.Equal(t, 1, published, bucket.String())
	}
	_, published := cache.counts(historical)
	require.Equal(t, 1, published)
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, listCalls)
}

func TestSchedulerFullRebuildUsesGroupLifecycleAuthority(t *testing.T) {
	registered := []SchedulerBucket{{GroupID: 0, Platform: "legacy-zero", Mode: "unknown"}, {GroupID: 106, Platform: "legacy-positive", Mode: "unknown"}, {GroupID: -8, Platform: "legacy-negative", Mode: "unknown"}}
	cache := newFullRebuildLifecycleCache(registered...)
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)
	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{106}, freshCalls)
	require.Equal(t, len(schedulerCanonicalBuckets(0))+2, cache.captureAttemptCount())
	require.Zero(t, providers.callCount())
	require.Equal(t, bucketStrings(append(schedulerCanonicalBuckets(106), registered[1])), bucketStrings(cache.retiredBuckets()))
	require.Empty(t, cache.tokens())
}

func TestSchedulerFullRebuildFreshReopenLockBusyRetriesWithoutBlockingOrdinaryTasks(t *testing.T) {
	const groupID int64 = 107
	canonical := schedulerBucketsForGroup(groupID)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	cache.lockBusyOnce[canonical[0].String()] = true
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	outbox := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 1, EventType: SchedulerOutboxEventFullRebuild}}}
	svc := newFullRebuildLifecycleService(cache, outbox, providers, groups)

	svc.pollOutbox()
	require.Zero(t, cache.currentWatermark())
	_, groupZeroPublished := cache.counts(schedulerCanonicalBuckets(0)[0])
	require.Equal(t, 1, groupZeroPublished, "ordinary tasks must still run when one strict Reopen task is busy")
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())

	svc.pollOutbox()
	require.Equal(t, int64(1), cache.currentWatermark())
	require.Equal(t, expectedCanonicalProviderQueryCount()*2, providers.callCount())
	_, busyBucketPublished := cache.counts(canonical[0])
	require.Equal(t, 1, busyBucketPublished)
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 2, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID}, freshCalls)
}

func TestSchedulerFullRebuildOrdinaryLockBusyKeepsExistingSkipSemantics(t *testing.T) {
	busyBucket := schedulerCanonicalBuckets(0)[0]
	cache := newFullRebuildLifecycleCache()
	cache.lockBusyOnce[busyBucket.String()] = true
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Zero(t, providers.callCount())
	attempts, published := cache.counts(busyBucket)
	require.Zero(t, attempts)
	require.Zero(t, published)
}

func TestSchedulerSnapshotServiceFullRebuildCoalescesConcurrentRequestsIntoTrailingRun(t *testing.T) {
	svc := &SnapshotService{}
	wantTrailingErr := errors.New("trailing rebuild failed")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseFirst) })
	}
	defer release()

	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	run := func() error {
		call := calls.Add(1)
		currentActive := active.Add(1)
		defer active.Add(-1)
		for {
			previousMax := maxActive.Load()
			if currentActive <= previousMax || maxActive.CompareAndSwap(previousMax, currentActive) {
				break
			}
		}
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		return wantTrailingErr
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- svc.coalesceFullRebuild(run)
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not start")
	}

	const followers = 20
	followerResults := make(chan error, followers)
	for range followers {
		go func() {
			followerResults <- svc.coalesceFullRebuild(run)
		}()
	}

	require.Eventually(t, func() bool {
		requested, _ := schedulerFullRebuildState(svc)
		return requested == followers+1
	}, time.Second, time.Millisecond)
	release()

	require.NoError(t, <-firstResult)
	for range followers {
		require.ErrorIs(t, <-followerResults, wantTrailingErr)
	}
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 1, maxActive.Load())
	requested, completed := schedulerFullRebuildState(svc)
	require.EqualValues(t, followers+1, requested)
	require.Equal(t, requested, completed)
}

func TestSchedulerSnapshotServiceFullRebuildRunsAgainForSequentialRequest(t *testing.T) {
	svc := &SnapshotService{}
	wantSecondErr := errors.New("second rebuild failed")
	var calls atomic.Int32
	run := func() error {
		if calls.Add(1) == 2 {
			return wantSecondErr
		}
		return nil
	}

	require.NoError(t, svc.coalesceFullRebuild(run))
	require.ErrorIs(t, svc.coalesceFullRebuild(run), wantSecondErr)
	require.EqualValues(t, 2, calls.Load())
	requested, completed := schedulerFullRebuildState(svc)
	require.EqualValues(t, 2, requested)
	require.Equal(t, requested, completed)
}

func TestSchedulerSnapshotServiceInitialFullRebuildFailsClosedWhenListBucketsFails(t *testing.T) {
	cache := &schedulerFullRebuildTestCache{listErr: errors.New("list buckets failed")}
	svc := NewSnapshotService(cache, nil, nil, nil, nil)

	svc.runInitialRebuild()

	cache.mu.Lock()
	listCalls := cache.listCalls
	captures := cache.captures
	lockCalls := cache.lockCalls
	cache.mu.Unlock()
	require.Equal(t, 1, listCalls)
	require.Zero(t, captures)
	require.Zero(t, lockCalls)
	requested, completed := schedulerFullRebuildState(svc)
	require.EqualValues(t, 1, requested)
	require.Equal(t, requested, completed)
	svc.fullRebuildStateMu.Lock()
	require.ErrorIs(t, svc.fullRebuildLastErr, cache.listErr)
	svc.fullRebuildStateMu.Unlock()
}

func TestSchedulerGroupLifecycleInactiveAndMissingRetireAllHistoricalBucketsWithoutProviderReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *SnapshotGroup
		err   error
	}{
		{name: "inactive", group: &SnapshotGroup{ID: 81, Status: StatusDisabled, Hydrated: true}},
		{name: "missing", err: ErrSnapshotGroupNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const groupID int64 = 81
			current := expectedGroupLifecycleBuckets(groupID)
			historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
			other := SchedulerBucket{GroupID: groupID + 1, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
			groupZero := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
			cache := newGroupLifecycleTestCache(current[0], historical, other, groupZero)
			groups := &groupLifecycleTestGroupRepo{group: tc.group, err: tc.err}
			providers := &groupLifecycleTestProviderRepo{}
			svc := newGroupLifecycleTestService(cache, providers, groups)
			seen := make(map[batchSeenKey]struct{})

			require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))

			expected := bucketStrings(append(current, historical))
			got := bucketStrings(cache.retiredBuckets())
			require.Equal(t, expected, got)
			retireHeld, _ := cache.lifecycleMutationLeaseStates()
			require.Len(t, retireHeld, len(expected))
			for _, held := range retireHeld {
				require.True(t, held)
			}
			require.NotContains(t, got, other.String())
			require.NotContains(t, got, groupZero.String())
			require.Zero(t, providers.callCount())
			require.Equal(t, 1, groups.callCount())
			_, _, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, listCalls)
			requireLifecycleSeen(t, seen, groupID)
		})
	}
}

func TestSchedulerPrepareGroupLifecycleUsesKnownHistoricalBucketsWithoutListingRegistry(t *testing.T) {
	const groupID int64 = 811
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
	cache := newGroupLifecycleTestCache()
	cache.listErr = errors.New("registry must not be listed")
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	plan, err := svc.prepareGroupLifecycle(context.Background(), groupID, []SchedulerBucket{historical})
	require.NoError(t, err)
	require.False(t, plan.active)
	require.Empty(t, plan.tasks)
	_, _, listCalls := cache.lifecycleCounts()
	require.Zero(t, listCalls)
	require.Contains(t, bucketStrings(cache.retiredBuckets()), historical.String())
	require.Zero(t, providers.callCount())
}

func TestSchedulerGroupLifecycleActiveReopensAndRebuildsAllCurrentBuckets(t *testing.T) {
	const groupID int64 = 82
	current := expectedGroupLifecycleBuckets(groupID)
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
	cache := newGroupLifecycleTestCache(historical)
	for _, bucket := range current {
		require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), bucket))
	}
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	providers.beforeLoad = func() {
		held, tokenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held, "the group lifecycle lease must be released before the first provider query")
		require.Equal(t, len(current), tokenCount, "all reopen tokens must be prepared before the first provider query")
	}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))

	require.Equal(t, bucketStrings(current), bucketStrings(cache.reopens))
	require.Empty(t, cache.retiredBuckets())
	registered, err := cache.retirementRaceCache.ListBuckets(context.Background())
	require.NoError(t, err)
	require.Contains(t, bucketStrings(registered), historical.String())
	require.Len(t, cache.tokens(), len(current))
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
	require.Equal(t, 1, providers.platformCallCount(PlatformOpenAI))
	for _, bucket := range current {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
	require.Contains(t, bucketStrings(current), SchedulerBucket{GroupID: groupID, Platform: PlatformAntigravity, Mode: SchedulerModeForced}.String())
	require.Contains(t, bucketStrings(current), SchedulerBucket{GroupID: groupID, Platform: "", Mode: SchedulerModeSingle}.String())
	require.Contains(t, bucketStrings(current), SchedulerBucket{GroupID: groupID, Platform: "", Mode: SchedulerModeForced}.String())
	acquires, releases, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, acquires)
	require.Equal(t, 1, releases)
	require.Zero(t, listCalls)
	require.Equal(t, schedulerGroupLifecycleLeaseTTL, cache.acquireTTL)
	require.True(t, cache.acquireDeadline)
	require.True(t, cache.releaseDeadline)
	require.NoError(t, cache.releaseCtxErr)
	_, reopenHeld := cache.lifecycleMutationLeaseStates()
	require.Len(t, reopenHeld, len(current))
	for _, held := range reopenHeld {
		require.True(t, held)
	}
	lockTTLs, unlockCalls := cache.lockStats()
	require.Len(t, lockTTLs, len(current))
	for _, ttl := range lockTTLs {
		require.Equal(t, 30*time.Second, ttl)
	}
	require.Equal(t, len(current), unlockCalls)
	requireLifecycleSeen(t, seen, groupID)
}

func TestSchedulerGroupLifecycleInactiveThenActiveAuthoritativelyReopens(t *testing.T) {
	const groupID int64 = 83
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	require.Zero(t, providers.callCount())
	groups.set(&SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))

	require.Len(t, cache.tokens(), len(expectedGroupLifecycleBuckets(groupID)))
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
	for _, bucket := range expectedGroupLifecycleBuckets(groupID) {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

func TestSchedulerGroupLifecycleLaterInactiveFencesLongActiveRebuild(t *testing.T) {
	const groupID int64 = 84
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
	started := make(chan struct{})
	release := make(chan struct{})
	providers := &groupLifecycleTestProviderRepo{started: started, release: release}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	activeSeen := make(map[batchSeenKey]struct{})
	inactiveSeen := make(map[batchSeenKey]struct{})
	activeResult := make(chan error, 1)

	go func() {
		activeResult <- svc.handleGroupEvent(context.Background(), ptrInt64(groupID), activeSeen)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active rebuild did not reach the provider load")
	}

	groups.set(&SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), inactiveSeen))
	close(release)
	err := <-activeResult
	require.ErrorIs(t, err, ErrSchedulerBucketRetired)
	requireLifecycleNotSeen(t, activeSeen, groupID)
	requireLifecycleSeen(t, inactiveSeen, groupID)
}

func TestSchedulerGroupLifecycleEpochPreventsABA(t *testing.T) {
	const groupID int64 = 85
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	groups.set(&SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	firstActiveTokens := cache.tokens()
	canonicalBucketCount := len(expectedGroupLifecycleBuckets(groupID))
	require.Len(t, firstActiveTokens, canonicalBucketCount)

	groups.set(&SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	groups.set(&SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	allTokens := cache.tokens()
	require.Len(t, allTokens, canonicalBucketCount*2)
	require.Greater(t, allTokens[canonicalBucketCount].Epoch, firstActiveTokens[0].Epoch)
	require.ErrorIs(t, cache.SetSnapshot(context.Background(), firstActiveTokens[0].Bucket, firstActiveTokens[0], nil), ErrSchedulerBucketWriteFenced)
}

func TestSchedulerGroupLifecycleSeenIsIndependentAndDeduplicatesGroupEvents(t *testing.T) {
	const groupID int64 = 86
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})
	for _, platform := range schedulerSnapshotPlatforms() {
		seen[batchSeenKey{groupID: groupID, platform: platform}] = struct{}{}
	}

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))
	require.Equal(t, 1, groups.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
	requireLifecycleSeen(t, seen, groupID)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))
	require.Equal(t, 1, groups.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
}

func TestSchedulerGroupLifecycleFailuresDoNotMarkSeen(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*groupLifecycleTestCache, *groupLifecycleTestGroupRepo, *groupLifecycleTestProviderRepo)
		check   func(*testing.T, error)
	}{
		{
			name: "lease busy",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseBusy = true
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, ErrSchedulerGroupLifecycleLeaseBusy) },
		},
		{
			name: "lease error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseAcquireErr = errors.New("lease failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "lease failed") },
		},
		{
			name: "release lost",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseReleaseErr = ErrSchedulerGroupLifecycleLeaseLost
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, ErrSchedulerGroupLifecycleLeaseLost) },
		},
		{
			name: "release error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseReleaseErr = errors.New("release failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "release failed") },
		},
		{
			name: "group query error",
			prepare: func(_ *groupLifecycleTestCache, groups *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				groups.err = errors.New("group query failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "group query failed") },
		},
		{
			name: "list buckets error",
			prepare: func(cache *groupLifecycleTestCache, groups *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				groups.group.Status = StatusDisabled
				cache.listErr = errors.New("list buckets failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "list buckets failed") },
		},
		{
			name: "retire bucket error",
			prepare: func(cache *groupLifecycleTestCache, groups *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				groups.group.Status = StatusDisabled
				cache.retireErr = errors.New("retire bucket failed")
				cache.retireErrAt = 2
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "retire bucket failed") },
		},
		{
			name: "reopen bucket error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.reopenErr = errors.New("reopen bucket failed")
				cache.reopenErrAt = 2
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "reopen bucket failed") },
		},
		{
			name: "provider rebuild error",
			prepare: func(_ *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, providers *groupLifecycleTestProviderRepo) {
				providers.err = errors.New("provider load failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "provider load failed") },
		},
		{
			name: "bucket lock busy",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.bucketLockBusy = true
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, ErrSchedulerBucketRebuildBusy) },
		},
		{
			name: "bucket lock error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.bucketLockErr = errors.New("bucket lock failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "bucket lock failed") },
		},
		{
			name: "set snapshot error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.setErr = errors.New("set snapshot failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "set snapshot failed") },
		},
	}

	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(870 + index)
			cache := newGroupLifecycleTestCache()
			groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
			providers := &groupLifecycleTestProviderRepo{}
			tc.prepare(cache, groups, providers)
			svc := newGroupLifecycleTestService(cache, providers, groups)
			seen := make(map[batchSeenKey]struct{})

			err := svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen)
			tc.check(t, err)
			requireLifecycleNotSeen(t, seen, groupID)
			if tc.name == "release lost" || tc.name == "release error" {
				require.Zero(t, providers.callCount())
			}
			if tc.name == "retire bucket error" || tc.name == "reopen bucket error" {
				_, releases, _ := cache.lifecycleCounts()
				require.Equal(t, 1, releases)
				require.Zero(t, providers.callCount())
			}
			if tc.name == "provider rebuild error" || tc.name == "set snapshot error" {
				lockTTLs, unlockCalls := cache.lockStats()
				require.Len(t, lockTTLs, 1)
				require.Equal(t, 1, unlockCalls)
				require.Equal(t, 1, providers.callCount())
			}
		})
	}
}

func TestSchedulerGroupLifecycleOperationAndReleaseErrorsPreserveBothCauses(t *testing.T) {
	const groupID int64 = 880
	operationErr := errors.New("group query failed")
	cache := newGroupLifecycleTestCache()
	cache.leaseReleaseErr = ErrSchedulerGroupLifecycleLeaseLost
	groups := &groupLifecycleTestGroupRepo{err: operationErr}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})

	err := svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen)
	require.ErrorIs(t, err, operationErr)
	require.ErrorIs(t, err, ErrSchedulerGroupLifecycleLeaseLost)
	requireLifecycleNotSeen(t, seen, groupID)
	require.Zero(t, providers.callCount())
}

func TestSchedulerGroupLifecycleUntrustedGroupStateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *SnapshotGroup
	}{
		{name: "not hydrated", group: &SnapshotGroup{ID: 88, Status: StatusActive}},
		{name: "mismatched id", group: &SnapshotGroup{ID: 89, Status: StatusActive, Hydrated: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const eventGroupID int64 = 88
			cache := newGroupLifecycleTestCache()
			groups := &groupLifecycleTestGroupRepo{group: tc.group}
			providers := &groupLifecycleTestProviderRepo{}
			svc := newGroupLifecycleTestService(cache, providers, groups)
			seen := make(map[batchSeenKey]struct{})

			err := svc.handleGroupEvent(context.Background(), ptrInt64(eventGroupID), seen)
			require.Error(t, err)
			require.Empty(t, cache.retiredBuckets())
			require.Empty(t, cache.tokens())
			require.Zero(t, providers.callCount())
			requireLifecycleNotSeen(t, seen, eventGroupID)
			acquires, releases, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, acquires)
			require.Equal(t, 1, releases)
			require.Zero(t, listCalls)
		})
	}
}

func TestSchedulerGroupLifecycleCanceledAfterFreshQueryUsesIndependentReleaseContext(t *testing.T) {
	const groupID int64 = 89
	ctx, cancel := context.WithCancel(context.Background())
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{
		group:    &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true},
		afterGet: cancel,
	}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})

	err := svc.handleGroupEvent(ctx, ptrInt64(groupID), seen)
	require.ErrorIs(t, err, context.Canceled)
	requireLifecycleNotSeen(t, seen, groupID)
	require.Empty(t, cache.tokens())
	require.Zero(t, providers.callCount())
	acquires, releases, _ := cache.lifecycleCounts()
	require.Equal(t, 1, acquires)
	require.Equal(t, 1, releases)
	require.True(t, cache.releaseDeadline)
	require.NoError(t, cache.releaseCtxErr)
}

func TestSchedulerGroupLifecycleUsesExplicitGroups(t *testing.T) {
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: 88, Status: StatusActive, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	require.NoError(t, svc.handleGroupEvent(context.Background(), nil, make(map[batchSeenKey]struct{})))
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(0), make(map[batchSeenKey]struct{})))
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(88), make(map[batchSeenKey]struct{})))

	acquires, releases, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, acquires)
	require.Equal(t, 1, releases)
	require.Zero(t, listCalls)
	require.Equal(t, 1, groups.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
}

func TestSchedulerSnapshotServicePollOutboxCleansConsumedRowsAfterWatermark(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{ID: 10000, EventType: SchedulerOutboxEventProviderLastUsed},
		},
		rows:         int64Range(1, 10003),
		lockAcquired: true,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if cache.watermark != 10000 {
		t.Fatalf("expected watermark 10000, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(cache.setWatermarks, []int64{10000}) {
		t.Fatalf("unexpected watermark writes: %#v", cache.setWatermarks)
	}
	if !reflect.DeepEqual(repo.rows, []int64{10001, 10002, 10003}) {
		t.Fatalf("expected rows above watermark to remain, got %#v", repo.rows)
	}
	if repo.lockAttempts != 1 || repo.releaseCount != 1 {
		t.Fatalf("expected one lock acquire/release, got acquire=%d release=%d", repo.lockAttempts, repo.releaseCount)
	}
	if len(repo.deleteCalls) != 3 {
		t.Fatalf("expected cleanup to loop until a short batch, got %d calls", len(repo.deleteCalls))
	}
	for _, call := range repo.deleteCalls {
		if call.watermark != 10000 || call.limit != schedulerOutboxCleanupBatch {
			t.Fatalf("unexpected cleanup call: %#v", call)
		}
	}
}

func TestSchedulerSnapshotServicePollOutboxSkipsCleanupWhenLockUnavailable(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{ID: 3, EventType: SchedulerOutboxEventProviderLastUsed},
		},
		rows:         []int64{1, 2, 3, 4},
		lockAcquired: false,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if cache.watermark != 3 {
		t.Fatalf("expected watermark 3, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3, 4}) {
		t.Fatalf("expected cleanup to skip all rows, got %#v", repo.rows)
	}
	if repo.lockAttempts != 1 {
		t.Fatalf("expected one lock attempt, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if repo.releaseCount != 0 {
		t.Fatalf("expected no release without lock, got %d", repo.releaseCount)
	}
}

func TestSchedulerSnapshotServicePollOutboxDoesNotCleanupOnHandleFailure(t *testing.T) {
	cache := &outboxCleanupCache{
		updateErr: errors.New("cache update failed"),
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{
				ID:        5,
				EventType: SchedulerOutboxEventProviderLastUsed,
				Payload: map[string]any{
					"last_used": map[string]any{"101": float64(123)},
				},
			},
		},
		rows:         []int64{1, 2, 3, 4, 5, 6},
		lockAcquired: true,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if len(cache.setWatermarks) != 0 {
		t.Fatalf("expected no watermark write on handle failure, got %#v", cache.setWatermarks)
	}
	if repo.lockAttempts != 0 {
		t.Fatalf("expected cleanup lock not to be attempted, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("expected rows unchanged, got %#v", repo.rows)
	}
}

func TestSchedulerSnapshotServicePollOutboxDoesNotUseConsumedEventForLag(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{
				ID:        7,
				EventType: SchedulerOutboxEventProviderLastUsed,
				CreatedAt: time.Now().Add(-time.Hour),
			},
		},
	}
	cfg := &SnapshotOptions{
		OutboxLagWarnSeconds:     1,
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	svc.pollOutbox()

	if cache.watermark != 7 {
		t.Fatalf("expected watermark 7, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(repo.firstCreatedAfterID, []int64{7}) {
		t.Fatalf("expected lag check after consumed watermark, got %#v", repo.firstCreatedAfterID)
	}
	if cache.listBucketCalls != 0 {
		t.Fatalf("expected consumed event not to trigger full rebuild, got %d attempts", cache.listBucketCalls)
	}
	if svc.lagFailures != 0 {
		t.Fatalf("expected lag failures to remain reset, got %d", svc.lagFailures)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagLatchesPersistentDegradation(t *testing.T) {
	tests := []struct {
		name             string
		createdAt        time.Time
		rows             []int64
		lagSeconds       int
		backlogThreshold int
	}{
		{
			name:       "lag",
			createdAt:  time.Now().Add(-time.Hour),
			rows:       []int64{1},
			lagSeconds: 1,
		},
		{
			name:             "backlog",
			createdAt:        time.Now(),
			rows:             []int64{100},
			backlogThreshold: 50,
		},
		{
			name:             "lag_and_backlog",
			createdAt:        time.Now().Add(-time.Hour),
			rows:             []int64{100},
			lagSeconds:       1,
			backlogThreshold: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &outboxCleanupCache{listBuckets: []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}}}
			repo := &outboxCleanupRepo{
				events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: tt.createdAt}},
				rows:   tt.rows,
			}
			cfg := &SnapshotOptions{
				OutboxLagRebuildSeconds:  tt.lagSeconds,
				OutboxLagRebuildFailures: 1,
				OutboxBacklogRebuildRows: tt.backlogThreshold,
			}
			svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

			for range 3 {
				svc.checkOutboxLag(context.Background(), 0)
			}

			if cache.listBucketCalls != 1 {
				t.Fatalf("expected one rebuild attempt during a persistent degraded episode, got %d", cache.listBucketCalls)
			}
		})
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRearmsAfterRecovery(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected a failed rebuild to stay bounded within the episode, got %d attempts", cache.listBucketCalls)
	}

	svc.checkOutboxLag(context.Background(), 1)
	repo.events = append(repo.events, SchedulerOutboxEvent{ID: 2, CreatedAt: time.Now().Add(-time.Hour)})
	repo.rows = []int64{2}
	svc.checkOutboxLag(context.Background(), 1)

	if cache.listBucketCalls != 2 {
		t.Fatalf("expected recovery to rearm a failed rebuild for the next episode, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRetriesAfterCooldownWithoutRecovery(t *testing.T) {
	cache := &outboxCleanupCache{
		listBucketErr: errors.New("list buckets failed"),
		listBuckets:   []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}},
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:    1,
		OutboxLagRebuildFailures:   1,
		FullRebuildIntervalSeconds: 0,
	}
	svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected failed rebuild polls to be rate limited, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	if !svc.outboxRebuildRetryAt.After(time.Now()) {
		t.Fatal("expected failed rebuild to schedule a future retry")
	}
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected persistent degradation to retry after cooldown, got %d attempts", cache.listBucketCalls)
	}

	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected repeated rebuild failures to stay rate limited, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	cache.listBucketErr = nil
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 3 {
		t.Fatalf("expected degraded episode to retry after cooldown, got %d attempts", cache.listBucketCalls)
	}

	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}
	if cache.listBucketCalls != 3 {
		t.Fatalf("expected successful retry to latch the degraded episode, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetryDoesNotBypassNewLagThreshold(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   []int64{100},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 3,
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// Start a failed backlog rebuild, then make its retry due.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the backlog degradation to attempt one rebuild, got %d", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()

	// After backlog recovery, lag observations start a separate threshold count.
	repo.rows = []int64{1}
	repo.events[0].CreatedAt = time.Now().Add(-time.Hour)
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the new lag episode to start at its own threshold, got %d rebuild attempts", cache.listBucketCalls)
	}

	svc.checkOutboxLag(context.Background(), 0)
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected lag rebuild only after three lag observations, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagLagRetryDoesNotDelayOrEscalateNewBacklog(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// Start a failed lag rebuild and leave its retry in cooldown.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the lag degradation to attempt one rebuild, got %d", cache.listBucketCalls)
	}

	// A new backlog episode starts immediately after lag recovery and begins its own failure count.
	repo.events[0].CreatedAt = time.Now()
	repo.rows = []int64{100}
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected the new backlog degradation not to inherit lag cooldown, got %d rebuild attempts", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	failures := svc.outboxRebuildFailures
	svc.lagMu.Unlock()
	if failures != 1 {
		t.Fatalf("expected backlog retry failures to restart at one, got %d", failures)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetrySurvivesUnknownBacklog(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   []int64{100},
	}
	cfg := &SnapshotOptions{
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// A failed backlog rebuild starts a cooldown specific to that reason.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected one initial backlog rebuild, got %d", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	retryAt := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if !retryAt.After(time.Now()) {
		t.Fatalf("expected a future backlog retry, got %s", retryAt)
	}

	// A transient MaxID failure leaves backlog health unknown.
	repo.maxIDErr = errors.New("max id unavailable")
	svc.checkOutboxLag(context.Background(), 0)
	svc.lagMu.Lock()
	retryReason := svc.outboxRebuildRetryReason
	failures := svc.outboxRebuildFailures
	retryAtAfterUnknown := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if retryReason != "outbox_backlog" || failures != 1 || !retryAtAfterUnknown.Equal(retryAt) {
		t.Fatalf("expected unknown backlog to preserve retry state, got reason=%q failures=%d retry_at=%s", retryReason, failures, retryAtAfterUnknown)
	}

	// Once MaxID recovers, a still-degraded backlog retains the cooldown
	// and retries after it expires.
	repo.maxIDErr = nil
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected backlog recovery before cooldown to stay rate limited, got %d attempts", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected backlog retry after cooldown expiry, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagPreemptsUnknownBacklogRetryAtThreshold(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   []int64{100},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 3,
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// Make backlog health unknown after its first failed rebuild.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected one initial backlog rebuild, got %d", cache.listBucketCalls)
	}
	repo.maxIDErr = errors.New("max id unavailable")
	repo.events[0].CreatedAt = time.Now().Add(-time.Hour)

	// Lag observations accumulate independently and trigger a rebuild at their own threshold.
	for observation := 1; observation <= 2; observation++ {
		svc.checkOutboxLag(context.Background(), 0)
		if cache.listBucketCalls != 1 {
			t.Fatalf("expected lag observation %d to stay below threshold, got %d rebuild attempts", observation, cache.listBucketCalls)
		}
	}
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected lag to preempt backlog cooldown at its threshold, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	retryReason := svc.outboxRebuildRetryReason
	failures := svc.outboxRebuildFailures
	retryAt := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if retryReason != "outbox_lag" || failures != 1 || !retryAt.After(time.Now()) {
		t.Fatalf("expected a fresh lag retry generation, got reason=%q failures=%d retry_at=%s", retryReason, failures, retryAt)
	}
}

func TestOutboxRebuildRetryDelayIsExponentiallyBounded(t *testing.T) {
	previous := time.Duration(0)
	for failures := 1; failures <= 20; failures++ {
		delay := outboxRebuildRetryDelay(failures)
		if delay < previous {
			t.Fatalf("expected retry delay to be monotonic, failure %d produced %s after %s", failures, delay, previous)
		}
		if delay > outboxRebuildRetryMaxDelay {
			t.Fatalf("expected retry delay to stay bounded, got %s", delay)
		}
		previous = delay
	}
	if previous != outboxRebuildRetryMaxDelay {
		t.Fatalf("expected repeated failures to reach max delay %s, got %s", outboxRebuildRetryMaxDelay, previous)
	}
}

func TestSchedulerSnapshotServicePollOutboxEmptyBatchClearsDegradedEpisode(t *testing.T) {
	cache := &outboxCleanupCache{listBuckets: []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}}}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 1,
	}
	svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	cache.watermark = 1
	svc.pollOutbox()

	if !reflect.DeepEqual(repo.firstCreatedAfterID, []int64{0}) {
		t.Fatalf("expected empty poll to use the empty batch as recovery evidence, got watermarks %#v", repo.firstCreatedAfterID)
	}
	if repo.maxIDCalls != 1 {
		t.Fatalf("expected empty poll to skip a redundant backlog query, got %d health checks", repo.maxIDCalls)
	}

	repo.events = append(repo.events, SchedulerOutboxEvent{ID: 2, CreatedAt: time.Now().Add(-time.Hour)})
	repo.rows = []int64{2}
	svc.checkOutboxLag(context.Background(), 1)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected empty-poll recovery to rearm the next degraded episode, got %d attempts", cache.listBucketCalls)
	}
}

func TestSchedulerSnapshotServiceOutboxLagWarningIsTransitionLimited(t *testing.T) {
	svc := NewSnapshotService(nil, nil, nil, nil, nil)

	if !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected the initial degraded transition to log")
	}
	if svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected persistent degradation to suppress repeated warnings")
	}
	if svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected persistent degradation to suppress repeated warnings")
	}
	if svc.shouldLogOutboxLagWarning(false) {
		t.Fatal("expected recovery not to emit a lag warning")
	}
	if !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected renewed degradation to log after recovery")
	}
}

func TestSchedulerSnapshotServiceCheckOutboxLagSamplesMaxIDErrors(t *testing.T) {
	svc := NewSnapshotService(nil, nil, nil, nil, nil)
	now := time.Now()

	if !svc.shouldLogOutboxMaxIDError(now) {
		t.Fatal("expected the first MaxID error to log")
	}
	if svc.shouldLogOutboxMaxIDError(now.Add(outboxMaxIDErrorLogSampleInterval / 2)) {
		t.Fatal("expected MaxID errors inside the sample interval to be suppressed")
	}
	if !svc.shouldLogOutboxMaxIDError(now.Add(outboxMaxIDErrorLogSampleInterval)) {
		t.Fatal("expected MaxID error logging to rearm after the sample interval")
	}
}

func TestSchedulerSnapshotServicePollOutboxHealthyEmptyBatchSkipsLagHealthQueries(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 1,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	svc.pollOutbox()

	if len(repo.firstCreatedAfterID) != 0 {
		t.Fatalf("expected healthy empty poll to skip lag query, got watermarks %#v", repo.firstCreatedAfterID)
	}
	if repo.maxIDCalls != 0 {
		t.Fatalf("expected healthy empty poll to skip backlog query, got %d calls", repo.maxIDCalls)
	}
}

func TestSchedulerSnapshotServiceEmptyPollDoesNotReleaseRunningRebuild(t *testing.T) {
	baseCache := &outboxCleanupCache{
		watermark:   1,
		listBuckets: []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}},
	}
	cache := &blockingOutboxCleanupCache{
		outboxCleanupCache: baseCache,
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}
	svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

	firstDone := make(chan struct{})
	go func() {
		svc.checkOutboxLag(context.Background(), 0)
		close(firstDone)
	}()
	select {
	case <-cache.started:
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not start")
	}

	// An empty poll clears episode and retry state while the active rebuild retains its execution slot.
	svc.pollOutbox()

	secondDone := make(chan struct{})
	go func() {
		svc.checkOutboxLag(context.Background(), 0)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
		close(cache.release)
		<-firstDone
		<-secondDone
		t.Fatal("second lag check queued another rebuild while the first was running")
	}

	close(cache.release)
	<-firstDone
	if calls := cache.listCalls(); calls != 1 {
		t.Fatalf("expected one rebuild generation, got %d", calls)
	}
}

func TestSchedulerSnapshotServiceCleanupSkipsNonPositiveWatermark(t *testing.T) {
	repo := &outboxCleanupRepo{
		rows:         []int64{1, 2, 3},
		lockAcquired: true,
	}
	svc := NewSnapshotService(&outboxCleanupCache{}, repo, nil, nil, nil)

	svc.cleanupConsumedOutbox(0)

	if repo.lockAttempts != 0 {
		t.Fatalf("expected no lock attempt for non-positive watermark, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3}) {
		t.Fatalf("expected rows unchanged, got %#v", repo.rows)
	}
}

func TestSchedulerFullRebuildCapturesAllRegistryTokensBeforeDBLoad(t *testing.T) {
	first := SchedulerBucket{GroupID: 61, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	queued := SchedulerBucket{GroupID: 61, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newRetirementRaceCache(first, queued)
	dbStarted := make(chan struct{})
	releaseDB := make(chan struct{})
	var firstDB sync.Once
	repo := &retirementProviderSource{
		listPlatformFunc: func(context.Context, string) ([]SnapshotProvider, error) {
			firstDB.Do(func() {
				close(dbStarted)
				<-releaseDB
			})
			return []SnapshotProvider{snapshotTestProvider{ID: 6101, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}, nil
		},
	}
	svc := NewSnapshotService(cache, nil, repo, &retirementGroupRepo{groups: []SnapshotGroup{{ID: 61, Status: StatusActive}}}, &SnapshotOptions{DbFallbackEnabled: true})

	result := make(chan error, 1)
	go func() { result <- svc.triggerFullRebuild("retirement_race_a") }()
	select {
	case <-dbStarted:
	case <-time.After(time.Second):
		t.Fatal("first DB load did not start")
	}

	captures, reopens := cache.captureAndReopenCounts()
	require.Equal(t, len(schedulerCanonicalBuckets(0))*2, captures, "group0 and active-group canonical tokens must be captured before the first DB load")
	require.Zero(t, reopens)
	require.NoError(t, cache.RetireBucket(context.Background(), queued))
	_, err := cache.ReopenBucket(context.Background(), queued)
	require.NoError(t, err)
	close(releaseDB)
	require.NoError(t, <-result)

	_, firstPublished := cache.counts(first)
	queuedAttempts, queuedPublished := cache.counts(queued)
	require.Equal(t, 1, firstPublished)
	require.Equal(t, 1, queuedAttempts)
	require.Zero(t, queuedPublished, "queued registry task must not adopt the reopened epoch")
}

func TestSchedulerRebuildRetireAfterDBLoadFencesPublish(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 62, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	dbReturned := make(chan struct{})
	setEntered := make(chan struct{})
	releaseSet := make(chan struct{})
	cache.beforeSet = func() {
		close(setEntered)
		<-releaseSet
	}
	repo := &retirementProviderSource{
		listPlatformFunc: func(context.Context, string) ([]SnapshotProvider, error) {
			close(dbReturned)
			return []SnapshotProvider{snapshotTestProvider{ID: 6201, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}, nil
		},
	}
	svc := NewSnapshotService(cache, nil, repo, nil, &SnapshotOptions{DbFallbackEnabled: true})

	result := make(chan error, 1)
	go func() {
		result <- svc.rebuildBuckets(context.Background(), []SchedulerBucket{bucket}, "retirement_race_b")
	}()
	select {
	case <-dbReturned:
	case <-time.After(time.Second):
		t.Fatal("DB load did not return")
	}
	select {
	case <-setEntered:
	case <-time.After(time.Second):
		t.Fatal("snapshot writer did not reach allocation boundary")
	}
	require.NoError(t, cache.RetireBucket(context.Background(), bucket))
	close(releaseSet)
	require.NoError(t, <-result)

	setAttempts, published := cache.counts(bucket)
	require.Equal(t, 1, setAttempts)
	require.Zero(t, published)
	require.Zero(t, cache.version(bucket), "retirement before allocation must not advance the snapshot version")
}

func TestSchedulerFallbackReturnsDBProvidersWhenBucketRetired(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 63, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	require.NoError(t, cache.RetireBucket(context.Background(), bucket))
	repo := &retirementProviderSource{
		providers: []SnapshotProvider{snapshotTestProvider{ID: 6301, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}},
	}
	svc := NewSnapshotService(cache, nil, repo, nil, &SnapshotOptions{DbFallbackEnabled: true})
	groupID := bucket.GroupID

	providers, useMixed, err := svc.ListSchedulableProviders(context.Background(), &groupID, bucket.Platform, false)
	require.NoError(t, err)
	require.False(t, useMixed)
	require.Len(t, providers, 1)
	setAttempts, published := cache.counts(bucket)
	require.Zero(t, setAttempts)
	require.Zero(t, published)
}

func TestSchedulerSnapshotService_ListSchedulableProvidersStopsWhenCacheContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &schedulerSnapshotFallbackRepoStub{}
	svc := NewSnapshotService(schedulerSnapshotContextCacheStub{}, nil, repo, nil, nil)

	providers, useMixed, err := svc.ListSchedulableProviders(ctx, nil, capability.PlatformOpenAI, false)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, providers)
	require.False(t, useMixed)
	require.Equal(t, 0, repo.calls)
}

func TestSchedulerSnapshotPlatformsIncludesQoder(t *testing.T) {
	require.Contains(t, schedulerSnapshotPlatforms(), capability.PlatformQoder)
}

func TestSchedulerSnapshotServiceCanonicalBucketsIncludesQoder(t *testing.T) {
	buckets := schedulerCanonicalBuckets(0)

	require.Contains(t, buckets, SchedulerBucket{GroupID: 0, Platform: capability.PlatformQoder, Mode: SchedulerModeSingle})
	require.Contains(t, buckets, SchedulerBucket{GroupID: 0, Platform: capability.PlatformQoder, Mode: SchedulerModeForced})
	require.NotContains(t, buckets, SchedulerBucket{GroupID: 0, Platform: capability.PlatformQoder, Mode: SchedulerModeMixed})
}

func TestSnapshotStartAfterStop(t *testing.T) {
	c := &planSnapshotCache{calls: make(chan struct{}, 4)}
	s := NewSnapshotService(c, nil, nil, nil, nil)
	s.Stop()
	s.Start()
	s.Stop()
	if len(c.calls) > 0 {
		t.Fatalf("停止后仍执行初始重建: calls=%d", len(c.calls))
	}
}

func TestSnapshotRepeatedStart(t *testing.T) {
	c := &planSnapshotCache{calls: make(chan struct{}, 4)}
	s := NewSnapshotService(c, nil, nil, nil, nil)
	s.Start()
	<-c.calls
	s.Start()
	s.Stop()
	if len(c.calls) > 0 {
		t.Fatal("重复 Start 再次执行初始重建")
	}
}

func TestSnapshotStopCancelsWork(t *testing.T) {
	c := &planSnapshotCache{calls: make(chan struct{}, 4), block: make(chan struct{})}
	s := NewSnapshotService(c, nil, nil, nil, nil)
	s.Start()
	<-c.calls
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("Stop 没有取消支持 context 的在途重建")
	}
	close(c.block)
	<-done
}

// TestSchedulerSnapshotService_UpdateProviderInCache covers publication, nil inputs, and cache errors.
func TestSchedulerSnapshotService_UpdateProviderInCache(t *testing.T) {
	t.Run("calls cache.SetProvider", func(t *testing.T) {
		cache := &updateSnapshotCache{}
		core := NewSnapshotService(cache, nil, nil, nil, nil)
		require.NoError(t, core.UpdateProviderInCache(t.Context(), updateSnapshotValue{123}))
		require.Len(t, cache.values, 1)
		require.Equal(t, int64(123), cache.values[0].SnapshotMetadata().ID)
	})
	t.Run("returns nil when cache is nil", func(t *testing.T) {
		core := NewSnapshotService(nil, nil, nil, nil, nil)
		require.NoError(t, core.UpdateProviderInCache(t.Context(), updateSnapshotValue{1}))
	})
	t.Run("returns nil when provider is nil", func(t *testing.T) {
		cache := &updateSnapshotCache{}
		core := NewSnapshotService(cache, nil, nil, nil, nil)
		require.NoError(t, core.UpdateProviderInCache(t.Context(), nil))
		require.Empty(t, cache.values)
	})
	t.Run("propagates cache error", func(t *testing.T) {
		expected := errors.New("cache error")
		core := NewSnapshotService(&updateSnapshotCache{err: expected}, nil, nil, nil, nil)
		require.ErrorIs(t, core.UpdateProviderInCache(t.Context(), updateSnapshotValue{1}), expected)
	})
}

func newBatchProviderQueryRepo() *batchProviderQueryRepo {
	return &batchProviderQueryRepo{
		calls:   make(map[batchProviderQueryKey]int),
		results: make(map[batchProviderQueryKey][]batchProviderQueryResult),
	}
}

func (r *batchProviderQueryRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{groupID: groupID, platform: platform})
}

func (r *batchProviderQueryRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{groupID: groupID, platform: platforms[0], mixed: true})
}

func (r *batchProviderQueryRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platform})
}

func (r *batchProviderQueryRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platforms[0], mixed: true})
}

func (r *batchProviderQueryRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]SnapshotProvider, error) {
	panic("unexpected ListModelAvailabilityCandidates call")
}

func (r *batchProviderQueryRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platform})
}

func (r *batchProviderQueryRepo) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platforms[0], mixed: true})
}

func (r *batchProviderQueryRepo) run(key batchProviderQueryKey) ([]SnapshotProvider, error) {
	r.mu.Lock()
	r.calls[key]++
	call := r.calls[key]
	results := r.results[key]
	beforeRun := r.beforeRun
	r.mu.Unlock()

	if beforeRun != nil {
		beforeRun(key)
	}
	if call <= len(results) {
		result := results[call-1]
		return append([]SnapshotProvider(nil), result.providers...), result.err
	}
	return []SnapshotProvider{snapshotTestProvider{
		ID:          int64(call),
		Name:        "source",
		Platform:    key.platform,
		Status:      StatusActive,
		Schedulable: true,
	}}, nil
}

func (r *batchProviderQueryRepo) callCount(key batchProviderQueryKey) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

func newBatchSnapshotProviderIDCache() *batchSnapshotProviderIDCache {
	return &batchSnapshotProviderIDCache{
		batchSnapshotCache: newBatchSnapshotCache(),
		fullCalls:          make(map[SchedulerBucket]int),
		idOnlyCalls:        make(map[SchedulerBucket]int),
		idOnlyError:        make(map[SchedulerBucket]error),
		fullLateErr:        make(map[SchedulerBucket]error),
	}
}

func (c *batchSnapshotProviderIDCache) SetSnapshotAndReturnProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) ([]int64, error) {
	c.reuseMu.Lock()
	c.fullCalls[bucket]++
	c.reuseMu.Unlock()
	if err := c.SetSnapshot(ctx, bucket, token, providers); err != nil {
		return nil, err
	}
	c.reuseMu.Lock()
	lateErr := c.fullLateErr[bucket]
	returnEmpty := c.returnEmpty
	c.reuseMu.Unlock()
	if lateErr != nil {
		return nil, lateErr
	}
	if returnEmpty {
		return []int64{}, nil
	}
	ids := make([]int64, 0, len(providers))
	for _, provider := range providers {
		ids = append(ids, snapshotTestData(provider).ID)
	}
	return ids, nil
}

func (c *batchSnapshotProviderIDCache) SetSnapshotByProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providerIDs []int64) error {
	c.reuseMu.Lock()
	c.idOnlyCalls[bucket]++
	err := c.idOnlyError[bucket]
	c.reuseMu.Unlock()
	if err != nil {
		return err
	}
	providers := make([]SnapshotProvider, 0, len(providerIDs))
	for _, id := range providerIDs {
		providers = append(providers, snapshotTestProvider{ID: id})
	}
	return c.SetSnapshot(ctx, bucket, token, providers)
}

func (c *batchSnapshotProviderIDCache) reuseCounts(bucket SchedulerBucket) (full, idOnly int) {
	c.reuseMu.Lock()
	defer c.reuseMu.Unlock()
	return c.fullCalls[bucket], c.idOnlyCalls[bucket]
}

func newBatchSnapshotCache() *batchSnapshotCache {
	return &batchSnapshotCache{
		captured:    make(map[SchedulerBucket]SchedulerBucketWriteToken),
		locks:       make(map[SchedulerBucket]int),
		lockBusy:    make(map[SchedulerBucket]bool),
		lockErrors:  make(map[SchedulerBucket]error),
		setErrors:   make(map[SchedulerBucket]error),
		setAttempts: make(map[SchedulerBucket]int),
		writes:      make(map[SchedulerBucket][]batchSnapshotWrite),
		versions:    make(map[SchedulerBucket]int),
	}
}

func (c *batchSnapshotCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextEpoch++
	token := SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.nextEpoch}
	c.captures = append(c.captures, bucket)
	c.captured[bucket] = token
	return token, nil
}

func (c *batchSnapshotCache) TryLockBucket(_ context.Context, bucket SchedulerBucket, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locks[bucket]++
	if err := c.lockErrors[bucket]; err != nil {
		return false, err
	}
	return !c.lockBusy[bucket], nil
}

func (c *batchSnapshotCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

func (c *batchSnapshotCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	if c.beforeSet != nil {
		c.beforeSet()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setAttempts[bucket]++
	if token != c.captured[bucket] || !token.ValidFor(bucket) {
		return ErrSchedulerBucketWriteFenced
	}
	if err := c.setErrors[bucket]; err != nil {
		return err
	}
	c.versions[bucket]++
	c.writes[bucket] = append(c.writes[bucket], batchSnapshotWrite{
		token:     token,
		providers: append([]SnapshotProvider(nil), providers...),
	})
	return nil
}

func (c *batchSnapshotCache) captureCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captures)
}

func (c *batchSnapshotCache) bucketState(bucket SchedulerBucket) (locks, attempts, version int, writes []batchSnapshotWrite) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.locks[bucket], c.setAttempts[bucket], c.versions[bucket], append([]batchSnapshotWrite(nil), c.writes[bucket]...)
}

func newBatchQueryTestService(cache SnapshotCache, providers SnapshotProviderSource) *SnapshotService {
	return NewSnapshotService(cache, nil, providers, nil, &SnapshotOptions{})
}

func (r *batchQueryBenchmarkRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]SnapshotProvider, error) {
	return r.providers, nil
}

func (c *batchQueryBenchmarkCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *batchQueryBenchmarkCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

func (c *batchQueryBenchmarkCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

func (c *batchQueryBenchmarkCache) SetSnapshot(_ context.Context, _ SchedulerBucket, _ SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	batchQueryBenchmarkProviderCount = len(providers)
	return nil
}

// AcquireBucketLease wraps the batch cache's bucket lock in a releasable lease.
func (c *batchSnapshotCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// AcquireBucketLease provides a bucket lease for the batch-query benchmark.
func (c *batchQueryBenchmarkCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

func newBulkEventProviderRepo(providers ...SnapshotProvider) *bulkEventProviderRepo {
	return &bulkEventProviderRepo{
		batchProviderQueryRepo: newBatchProviderQueryRepo(),
		providers:              providers,
	}
}

func (r *bulkEventProviderRepo) GetByIDs(context.Context, []int64) ([]SnapshotProvider, error) {
	return append([]SnapshotProvider(nil), r.providers...), nil
}

func newBulkEventSnapshotCache() *bulkEventSnapshotCache {
	return &bulkEventSnapshotCache{batchSnapshotCache: newBatchSnapshotCache()}
}

func (c *bulkEventSnapshotCache) SetProvider(_ context.Context, provider SnapshotProvider) error {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	c.setProviderIDs = append(c.setProviderIDs, snapshotTestData(provider).ID)
	return nil
}

func (c *bulkEventSnapshotCache) DeleteProvider(_ context.Context, providerID int64) error {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	c.deleteProviderIDs = append(c.deleteProviderIDs, providerID)
	return nil
}

func (c *bulkEventSnapshotCache) providerWrites() (set []int64, deleted []int64) {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	return append([]int64(nil), c.setProviderIDs...), append([]int64(nil), c.deleteProviderIDs...)
}

func (c *bulkEventSnapshotCache) capturedBuckets() []SchedulerBucket {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SchedulerBucket(nil), c.captures...)
}

func newBulkEventTestService(cache SnapshotCache, providers SnapshotProviderSource) *SnapshotService {
	return NewSnapshotService(cache, nil, providers, nil, &SnapshotOptions{})
}

func bulkEventPayload(providerIDs []int64, groupIDs []int64) map[string]any {
	providerValues := make([]any, 0, len(providerIDs))
	for _, id := range providerIDs {
		providerValues = append(providerValues, id)
	}
	groupValues := make([]any, 0, len(groupIDs))
	for _, id := range groupIDs {
		groupValues = append(groupValues, id)
	}
	return map[string]any{
		"provider_ids": providerValues,
		"group_ids":    groupValues,
	}
}

func schedulerBucketsForTest(groupIDs []int64, platforms ...string) []SchedulerBucket {
	if !slices.Contains(platforms, "") {
		platforms = append([]string{""}, platforms...)
	}
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*len(platforms)*2)
	for _, platform := range platforms {
		for _, groupID := range groupIDs {
			buckets = append(buckets,
				SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
				SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
			)
		}
	}
	return buckets
}

func newFullRebuildLifecycleCache(buckets ...SchedulerBucket) *fullRebuildLifecycleCache {
	return &fullRebuildLifecycleCache{
		groupLifecycleTestCache: newGroupLifecycleTestCache(buckets...),
		captureErrors:           make(map[string]error),
		lockBusyOnce:            make(map[string]bool),
	}
}

func (c *fullRebuildLifecycleCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	c.captureAttempts = append(c.captureAttempts, bucket)
	err := c.captureErrors[bucket.String()]
	c.mu.Unlock()
	if err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	return c.retirementRaceCache.CaptureBucketWriteToken(ctx, bucket)
}

func (c *fullRebuildLifecycleCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	buckets, err := c.groupLifecycleTestCache.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	c.retirementRaceCache.mu.Lock()
	defer c.retirementRaceCache.mu.Unlock()
	registered := make([]SchedulerBucket, 0, len(buckets))
	for _, bucket := range buckets {
		if !c.retired[bucket.String()] {
			registered = append(registered, bucket)
		}
	}
	return registered, nil
}

func (c *fullRebuildLifecycleCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	busy := c.lockBusyOnce[bucket.String()]
	if busy {
		delete(c.lockBusyOnce, bucket.String())
	}
	c.mu.Unlock()
	if busy {
		return false, nil
	}
	return c.groupLifecycleTestCache.TryLockBucket(ctx, bucket, ttl)
}

func (c *fullRebuildLifecycleCache) GetOutboxWatermark(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark, nil
}

func (c *fullRebuildLifecycleCache) SetOutboxWatermark(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watermark = id
	c.watermarkWrites = append(c.watermarkWrites, id)
	return nil
}

func (c *fullRebuildLifecycleCache) captureAttemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captureAttempts)
}

func (c *fullRebuildLifecycleCache) currentWatermark() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark
}

func (c *fullRebuildLifecycleCache) totalSetAttempts() int {
	c.retirementRaceCache.mu.Lock()
	defer c.retirementRaceCache.mu.Unlock()
	var total int
	for _, attempts := range c.setAttempts {
		total += attempts
	}
	return total
}

func (r *fullRebuildLifecycleGroupRepo) ListActiveIDs(context.Context) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeIDCalls++
	return append([]int64(nil), r.activeIDs...), r.activeIDsErr
}

func (r *fullRebuildLifecycleGroupRepo) ListActive(context.Context) ([]SnapshotGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listActiveCalls++
	return nil, r.listActiveErr
}

func (r *fullRebuildLifecycleGroupRepo) GetByIDLite(_ context.Context, id int64) (*SnapshotGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.freshCalls = append(r.freshCalls, id)
	if err := r.freshErr[id]; err != nil {
		return nil, err
	}
	group := r.fresh[id]
	if group == nil {
		return nil, ErrSnapshotGroupNotFound
	}
	copyGroup := *group
	return &copyGroup, nil
}

func (r *fullRebuildLifecycleGroupRepo) stats() (activeIDs, listActive int, fresh []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeIDCalls, r.listActiveCalls, append([]int64(nil), r.freshCalls...)
}

func (r *fullRebuildFallbackGroupRepo) ListActive(context.Context) ([]SnapshotGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	return append([]SnapshotGroup(nil), r.groups...), r.err
}

func (r *fullRebuildProviderRepo) record(groupID int64, platform string) ([]SnapshotProvider, error) {
	r.mu.Lock()
	r.calls = append(r.calls, fullRebuildProviderCall{groupID: groupID, platform: platform})
	beforeFirst := r.beforeFirst
	r.mu.Unlock()
	if beforeFirst != nil {
		r.once.Do(beforeFirst)
	}
	return []SnapshotProvider{snapshotTestProvider{ID: 1, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

func (r *fullRebuildProviderRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]SnapshotProvider, error) {
	return r.record(groupID, platform)
}

func (r *fullRebuildProviderRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]SnapshotProvider, error) {
	return r.record(groupID, firstPlatform(platforms))
}

func (r *fullRebuildProviderRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.record(0, platform)
}

func (r *fullRebuildProviderRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.record(0, firstPlatform(platforms))
}

func (r *fullRebuildProviderRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]SnapshotProvider, error) {
	panic("unexpected ListModelAvailabilityCandidates call")
}

func (r *fullRebuildProviderRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.record(0, platform)
}

func (r *fullRebuildProviderRepo) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.record(0, firstPlatform(platforms))
}

func (r *fullRebuildProviderRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fullRebuildProviderRepo) groupCallCount(groupID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	for _, call := range r.calls {
		if call.groupID == groupID {
			count++
		}
	}
	return count
}

func firstPlatform(platforms []string) string {
	if len(platforms) == 0 {
		return ""
	}
	return platforms[0]
}

func newFullRebuildLifecycleService(
	cache SnapshotCache,
	outbox SchedulerOutboxRepository,
	providers SnapshotProviderSource, groups SnapshotGroupSource,
) *SnapshotService {
	return NewSnapshotService(cache, outbox, providers, groups, &SnapshotOptions{})
}

func requireFullRebuildNoMutationOrDB(t *testing.T, cache *fullRebuildLifecycleCache, providers *fullRebuildProviderRepo) {
	t.Helper()
	require.Zero(t, cache.captureAttemptCount())
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	require.Zero(t, providers.callCount())
	require.Zero(t, cache.totalSetAttempts())
}

// AcquireBucketLease attaches an unlock callback after the full-rebuild fixture acquires a lock.
func (c *fullRebuildLifecycleCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

func (c *schedulerFullRebuildTestCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listCalls++
	return nil, c.listErr
}

func (c *schedulerFullRebuildTestCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockCalls++
	return false, nil
}

func (c *schedulerFullRebuildTestCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	c.captures++
	c.mu.Unlock()
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *schedulerFullRebuildTestCache) ReopenBucket(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func schedulerFullRebuildState(svc *SnapshotService) (requested uint64, completed uint64) {
	svc.fullRebuildStateMu.Lock()
	defer svc.fullRebuildStateMu.Unlock()
	return svc.fullRebuildRequested, svc.fullRebuildCompleted
}

// AcquireBucketLease returns the full-rebuild fixture's lock acquisition result.
func (c *schedulerFullRebuildTestCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	return nil, ok, err
}

func newGroupLifecycleTestCache(buckets ...SchedulerBucket) *groupLifecycleTestCache {
	return &groupLifecycleTestCache{retirementRaceCache: newRetirementRaceCache(buckets...)}
}

func (c *groupLifecycleTestCache) TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.acquireCalls++
	c.acquireTTL = ttl
	_, c.acquireDeadline = ctx.Deadline()
	if c.leaseAcquireErr != nil {
		return SchedulerGroupLifecycleLease{}, false, c.leaseAcquireErr
	}
	if c.leaseBusy || c.leaseHeld {
		return SchedulerGroupLifecycleLease{}, false, nil
	}
	c.leaseSequence++
	c.lease = SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: fmt.Sprintf("owner-%d", c.leaseSequence)}
	c.leaseHeld = true
	return c.lease, true, nil
}

func (c *groupLifecycleTestCache) ReleaseGroupLifecycleLease(ctx context.Context, lease SchedulerGroupLifecycleLease) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.releaseCalls++
	_, c.releaseDeadline = ctx.Deadline()
	c.releaseCtxErr = ctx.Err()
	if c.leaseReleaseErr != nil {
		return c.leaseReleaseErr
	}
	if !c.leaseHeld || lease != c.lease {
		return ErrSchedulerGroupLifecycleLeaseLost
	}
	c.leaseHeld = false
	return nil
}

func (c *groupLifecycleTestCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	c.stateMu.Lock()
	c.retireCalls = append(c.retireCalls, bucket)
	c.retireHeld = append(c.retireHeld, c.leaseHeld)
	held := c.leaseHeld
	call := len(c.retireCalls)
	err := c.retireErr
	errAt := c.retireErrAt
	c.stateMu.Unlock()
	if !held {
		return errors.New("retire called outside group lifecycle lease")
	}
	if err != nil && (errAt <= 0 || call == errAt) {
		return err
	}
	return c.retirementRaceCache.RetireBucket(ctx, bucket)
}

func (c *groupLifecycleTestCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	if err := ctx.Err(); err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	c.stateMu.Lock()
	c.reopenHeld = append(c.reopenHeld, c.leaseHeld)
	held := c.leaseHeld
	call := len(c.reopenHeld)
	reopenErr := c.reopenErr
	reopenErrAt := c.reopenErrAt
	c.stateMu.Unlock()
	if !held {
		return SchedulerBucketWriteToken{}, errors.New("reopen called outside group lifecycle lease")
	}
	if reopenErr != nil && (reopenErrAt <= 0 || call == reopenErrAt) {
		return SchedulerBucketWriteToken{}, reopenErr
	}
	token, err := c.retirementRaceCache.ReopenBucket(ctx, bucket)
	if err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	c.stateMu.Lock()
	c.reopenTokens = append(c.reopenTokens, token)
	c.stateMu.Unlock()
	return token, nil
}

func (c *groupLifecycleTestCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.stateMu.Lock()
	c.listCalls++
	err := c.listErr
	c.stateMu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.retirementRaceCache.ListBuckets(ctx)
}

func (c *groupLifecycleTestCache) TryLockBucket(_ context.Context, _ SchedulerBucket, ttl time.Duration) (bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.bucketLockTTLs = append(c.bucketLockTTLs, ttl)
	if c.bucketLockErr != nil {
		return false, c.bucketLockErr
	}
	return !c.bucketLockBusy, nil
}

func (c *groupLifecycleTestCache) UnlockBucket(context.Context, SchedulerBucket) error {
	c.stateMu.Lock()
	c.unlockCalls++
	c.stateMu.Unlock()
	return nil
}

func (c *groupLifecycleTestCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	c.stateMu.Lock()
	err := c.setErr
	c.stateMu.Unlock()
	if err != nil {
		return err
	}
	return c.retirementRaceCache.SetSnapshot(ctx, bucket, token, providers)
}

func (c *groupLifecycleTestCache) lifecycleCounts() (acquires, releases, listCalls int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.acquireCalls, c.releaseCalls, c.listCalls
}

func (c *groupLifecycleTestCache) retiredBuckets() []SchedulerBucket {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]SchedulerBucket(nil), c.retireCalls...)
}

func (c *groupLifecycleTestCache) tokens() []SchedulerBucketWriteToken {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]SchedulerBucketWriteToken(nil), c.reopenTokens...)
}

func (c *groupLifecycleTestCache) leaseHeldAndTokenCount() (bool, int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.leaseHeld, len(c.reopenTokens)
}

func (c *groupLifecycleTestCache) lockStats() ([]time.Duration, int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]time.Duration(nil), c.bucketLockTTLs...), c.unlockCalls
}

func (c *groupLifecycleTestCache) lifecycleMutationLeaseStates() (retire, reopen []bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]bool(nil), c.retireHeld...), append([]bool(nil), c.reopenHeld...)
}

func (r *groupLifecycleTestGroupRepo) GetByIDLite(context.Context, int64) (*SnapshotGroup, error) {
	r.mu.Lock()
	r.calls++
	if r.err != nil {
		err := r.err
		r.mu.Unlock()
		return nil, err
	}
	if r.group == nil {
		r.mu.Unlock()
		return nil, ErrSnapshotGroupNotFound
	}
	copyGroup := *r.group
	afterGet := r.afterGet
	r.mu.Unlock()
	if afterGet != nil {
		afterGet()
	}
	return &copyGroup, nil
}

func (r *groupLifecycleTestGroupRepo) set(group *SnapshotGroup, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.group = group
	r.err = err
}

func (r *groupLifecycleTestGroupRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *groupLifecycleTestProviderRepo) load(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	r.mu.Lock()
	r.calls++
	if r.callsByPlatform == nil {
		r.callsByPlatform = make(map[string]int)
	}
	r.callsByPlatform[platform]++
	err := r.err
	started := r.started
	release := r.release
	r.mu.Unlock()
	if started != nil {
		r.once.Do(func() { close(started) })
	}
	if r.beforeLoad != nil {
		r.beforeLoadOnce.Do(r.beforeLoad)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return []SnapshotProvider{snapshotTestProvider{ID: 9001, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

func (r *groupLifecycleTestProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]SnapshotProvider, error) {
	return r.load(ctx, platform)
}

func (r *groupLifecycleTestProviderRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, _ int64, platforms []string) ([]SnapshotProvider, error) {
	platform := "mixed"
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	return r.load(ctx, platform)
}

func (r *groupLifecycleTestProviderRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *groupLifecycleTestProviderRepo) platformCallCount(platform string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callsByPlatform[platform]
}

func newGroupLifecycleTestService(cache SnapshotCache, providers SnapshotProviderSource, groups SnapshotGroupSource) *SnapshotService {
	return NewSnapshotService(cache, nil, providers, groups, &SnapshotOptions{})
}

func expectedGroupLifecycleBuckets(groupID int64) []SchedulerBucket {
	buckets := make([]SchedulerBucket, 0, len(schedulerSnapshotPlatforms())*3)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets,
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
		)
	}
	return buckets
}

func expectedCanonicalProviderQueryCount() int {
	// Single and forced buckets share one query per platform; the empty platform queries all group
	// providers.
	return len(schedulerSnapshotPlatforms())
}

func bucketStrings(buckets []SchedulerBucket) map[string]struct{} {
	out := make(map[string]struct{}, len(buckets))
	for _, bucket := range buckets {
		out[bucket.String()] = struct{}{}
	}
	return out
}

func requireLifecycleSeen(t *testing.T, seen map[batchSeenKey]struct{}, groupID int64) {
	t.Helper()
	_, ok := seen[batchSeenKey{groupID: groupID, lifecycle: true}]
	require.True(t, ok)
	for _, platform := range schedulerSnapshotPlatforms() {
		_, ok = seen[batchSeenKey{groupID: groupID, platform: platform}]
		require.True(t, ok)
	}
}

func requireLifecycleNotSeen(t *testing.T, seen map[batchSeenKey]struct{}, groupID int64) {
	t.Helper()
	_, ok := seen[batchSeenKey{groupID: groupID, lifecycle: true}]
	require.False(t, ok)
	for _, platform := range schedulerSnapshotPlatforms() {
		_, ok = seen[batchSeenKey{groupID: groupID, platform: platform}]
		require.False(t, ok)
	}
}

// AcquireBucketLease gives the group-lifecycle fixture a lease that releases its bucket lock.
func (c *groupLifecycleTestCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

func (c *outboxCleanupCache) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]SnapshotProvider, bool, error) {
	return nil, false, nil
}

func (c *outboxCleanupCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *outboxCleanupCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	return nil
}

func (c *outboxCleanupCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *outboxCleanupCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *outboxCleanupCache) TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	return SchedulerGroupLifecycleLease{}, false, nil
}

func (c *outboxCleanupCache) ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error {
	return nil
}

func (c *outboxCleanupCache) GetProvider(ctx context.Context, providerID int64) (SnapshotProvider, error) {
	return nil, nil
}

func (c *outboxCleanupCache) SetProvider(ctx context.Context, provider SnapshotProvider) error {
	return nil
}

func (c *outboxCleanupCache) DeleteProvider(ctx context.Context, providerID int64) error {
	return nil
}

func (c *outboxCleanupCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return c.updateErr
}

func (c *outboxCleanupCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	return true, nil
}

func (c *outboxCleanupCache) UnlockBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *outboxCleanupCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.listBucketCalls++
	return c.listBuckets, c.listBucketErr
}

func (c *outboxCleanupCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return c.watermark, nil
}

func (c *outboxCleanupCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	c.watermark = id
	c.setWatermarks = append(c.setWatermarks, id)
	return nil
}

func (r *outboxCleanupProviderRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]SnapshotProvider, error) {
	return nil, nil
}

func (c *blockingOutboxCleanupCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		close(c.started)
		<-c.release
	}
	return c.listBuckets, c.listBucketErr
}

func (c *blockingOutboxCleanupCache) listCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (r *outboxCleanupRepo) ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]SchedulerOutboxEvent, error) {
	events := make([]SchedulerOutboxEvent, 0, len(r.events))
	for _, event := range r.events {
		if event.ID <= afterID {
			continue
		}
		events = append(events, event)
		if limit > 0 && len(events) >= limit {
			break
		}
	}
	return events, nil
}

func (r *outboxCleanupRepo) FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error) {
	r.firstCreatedAfterID = append(r.firstCreatedAfterID, afterID)
	for _, event := range r.events {
		if event.ID > afterID {
			return event.CreatedAt, true, nil
		}
	}
	return time.Time{}, false, nil
}

func (r *outboxCleanupRepo) MaxID(ctx context.Context) (int64, error) {
	r.maxIDCalls++
	if r.maxIDErr != nil {
		return 0, r.maxIDErr
	}
	var maxID int64
	for _, id := range r.rows {
		if id > maxID {
			maxID = id
		}
	}
	return maxID, nil
}

func (r *outboxCleanupRepo) DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error) {
	r.deleteCalls = append(r.deleteCalls, outboxCleanupDeleteCall{
		watermark: watermark,
		limit:     limit,
	})
	if watermark <= 0 || limit <= 0 {
		return 0, nil
	}

	deleted := int64(0)
	kept := make([]int64, 0, len(r.rows))
	for _, id := range r.rows {
		if id <= watermark && deleted < int64(limit) {
			deleted++
			continue
		}
		kept = append(kept, id)
	}
	r.rows = kept
	return deleted, nil
}

func (r *outboxCleanupRepo) TryAcquireCleanupLock(ctx context.Context) (SchedulerOutboxCleanupLease, bool, error) {
	r.lockAttempts++
	if !r.lockAcquired {
		return nil, false, nil
	}
	return outboxCleanupLease{release: func() {
		r.releaseCount++
	}}, true, nil
}

func (l outboxCleanupLease) Release() {
	if l.release != nil {
		l.release()
	}
}

func int64Range(start, end int64) []int64 {
	values := make([]int64, 0, end-start+1)
	for id := start; id <= end; id++ {
		values = append(values, id)
	}
	return values
}

// AcquireBucketLease provides a releasable bucket lock for outbox cleanup tests.
func (c *outboxCleanupCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

func newRetirementRaceCache(buckets ...SchedulerBucket) *retirementRaceCache {
	return &retirementRaceCache{
		epochs:      make(map[string]int64),
		retired:     make(map[string]bool),
		listBuckets: buckets,
		setAttempts: make(map[string]int),
		published:   make(map[string]int),
		versions:    make(map[string]int),
	}
}

func (c *retirementRaceCache) GetSnapshot(context.Context, SchedulerBucket) ([]SnapshotProvider, bool, error) {
	return nil, false, nil
}

func (c *retirementRaceCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	c.captures = append(c.captures, bucket)
	if c.retired[key] {
		return SchedulerBucketWriteToken{}, ErrSchedulerBucketRetired
	}
	if c.epochs[key] == 0 {
		c.epochs[key] = 1
	}
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.epochs[key]}, nil
}

func (c *retirementRaceCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, _ []SnapshotProvider) error {
	if c.beforeSet != nil {
		c.beforeSet()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	c.setAttempts[key]++
	if !token.ValidFor(bucket) {
		return ErrSchedulerBucketWriteFenced
	}
	if c.retired[key] {
		return ErrSchedulerBucketRetired
	}
	if c.epochs[key] != token.Epoch {
		return ErrSchedulerBucketWriteFenced
	}
	c.versions[key]++
	c.published[key]++
	return nil
}

func (c *retirementRaceCache) RetireBucket(_ context.Context, bucket SchedulerBucket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	if !c.retired[key] {
		c.epochs[key]++
		if c.epochs[key] < 1 {
			c.epochs[key] = 1
		}
		c.retired[key] = true
	}
	return nil
}

func (c *retirementRaceCache) ReopenBucket(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	if c.epochs[key] == 0 {
		c.epochs[key] = 1
	}
	delete(c.retired, key)
	c.reopens = append(c.reopens, bucket)
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.epochs[key]}, nil
}

func (c *retirementRaceCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

func (c *retirementRaceCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

func (c *retirementRaceCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return append([]SchedulerBucket(nil), c.listBuckets...), nil
}

func (c *retirementRaceCache) counts(bucket SchedulerBucket) (setAttempts, published int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setAttempts[bucket.String()], c.published[bucket.String()]
}

func (c *retirementRaceCache) captureAndReopenCounts() (captures, reopens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captures), len(c.reopens)
}

func (c *retirementRaceCache) version(bucket SchedulerBucket) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.versions[bucket.String()]
}

func (r *retirementGroupRepo) ListActive(context.Context) ([]SnapshotGroup, error) {
	return r.groups, r.err
}

// AcquireBucketLease registers bucket-lock cleanup for retirement race tests.
func (c *retirementRaceCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

func (s schedulerSnapshotContextCacheStub) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]SnapshotProvider, bool, error) {
	return nil, false, ctx.Err()
}

func (r *schedulerSnapshotFallbackRepoStub) ListSchedulableByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	r.calls++
	return nil, nil
}

func (r *schedulerSnapshotFallbackRepoStub) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	r.calls++
	return nil, nil
}

func (a snapshotTestProvider) SnapshotMetadata() SnapshotMetadata {
	return SnapshotMetadata{ID: a.ID, Name: a.Name, Platform: a.Platform, GroupIDs: slices.Clone(a.GroupIDs)}
}

func snapshotTestData(value SnapshotProvider) *snapshotTestProvider {
	switch v := value.(type) {
	case snapshotTestProvider:
		return &v
	case *snapshotTestProvider:
		return v
	default:
		panic("unexpected snapshot fixture")
	}
}

// ptrInt64 returns an optional group ID for tests.
func ptrInt64(value int64) *int64 { return &value }

func (r *retirementProviderSource) ListSchedulableByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	if r.listPlatformFunc != nil {
		return r.listPlatformFunc(ctx, platform)
	}
	var out []SnapshotProvider
	for _, a := range r.providers {
		if a.SnapshotMetadata().Platform == platform {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *retirementProviderSource) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *retirementProviderSource) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func (r *retirementProviderSource) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]SnapshotProvider, error) {
	var out []SnapshotProvider
	for _, a := range r.providers {
		for _, platform := range platforms {
			if a.SnapshotMetadata().Platform == platform {
				out = append(out, a)
				break
			}
		}
	}
	return out, nil
}

func (r *retirementProviderSource) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, _ int64, platforms []string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatforms(ctx, platforms)
}

func (r *retirementProviderSource) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatforms(ctx, platforms)
}

func (s *planSnapshotCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	s.calls <- struct{}{}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("snapshot probe failed")
}

func (v updateSnapshotValue) SnapshotMetadata() SnapshotMetadata {
	return SnapshotMetadata{ID: v.id}
}

func (c *updateSnapshotCache) SetProvider(_ context.Context, value SnapshotProvider) error {
	c.values = append(c.values, value)
	return c.err
}
