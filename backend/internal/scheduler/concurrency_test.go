package scheduler

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	_ ConcurrencyCache          = (*stubConcurrencyCacheForTest)(nil)
	_ OpenAIWSIngressLeaseCache = (*ingressLeaseCacheForTest)(nil)
)

type slotCleanupCache struct {
	ConcurrencyCache
	calls atomic.Int64
}

// stubConcurrencyCacheForTest records cache calls and supplies concurrency test results.
type stubConcurrencyCacheForTest struct {
	acquireResult        bool
	acquireErr           error
	releaseErr           error
	concurrency          int
	concurrencyErr       error
	waitAllowed          bool
	waitErr              error
	waitCount            int
	waitCountErr         error
	loadBatch            map[int64]*ProviderLoadInfo
	loadBatchErr         error
	usersLoadBatch       map[int64]*UserLoadInfo
	usersLoadErr         error
	cleanupErr           error
	apiKeyTrackErr       error
	apiKeyReleaseErr     error
	apiKeyConcurrency    map[int64]int
	apiKeyConcurrencyErr error

	// Recorded calls.
	releasedProviderIDs      []int64
	releasedRequestIDs       []string
	loadBatchCalls           atomic.Int64
	trackedAPIKeyIDs         []int64
	trackedAPIKeyRequestIDs  []string
	releasedAPIKeyIDs        []int64
	releasedAPIKeyRequestIDs []string
}

type ingressLeaseCacheForTest struct {
	stubConcurrencyCacheForTest
	acquireIngressResult bool
	acquireIngressErr    error
	acquireIngressFn     func(context.Context, int64, int, string) (bool, error)
	refreshIngressResult bool
	refreshIngressErr    error
	refreshIngressFn     func(context.Context, int64, string) (bool, error)
	releaseIngressErr    error
	releaseIngressFn     func(context.Context, int64, string) error
	acquireIngressCalls  int
	refreshIngressCalls  int
	releaseIngressCalls  int
}

type trackingConcurrencyCache struct {
	stubConcurrencyCacheForTest
	cleanupPrefix string
}

func TestStartSlotCleanupWorker_UsesCacheWideCleanupWithoutProviderRepo(t *testing.T) {
	cache := &slotCleanupCache{}
	svc := NewConcurrencyService(cache)
	t.Cleanup(svc.Stop)

	svc.StartSlotCleanupWorker(time.Hour)

	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cache.calls.Load() > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("cleanup worker did not call cache-wide provider slot cleanup")
		case <-ticker.C:
		}
	}
}

func TestCleanupStaleProcessSlots_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	require.NoError(t, svc.CleanupStaleProcessSlots(context.Background()))
}

func TestCleanupStaleProcessSlots_DelegatesPrefix(t *testing.T) {
	cache := &trackingConcurrencyCache{}
	svc := NewConcurrencyService(cache)
	require.NoError(t, svc.CleanupStaleProcessSlots(context.Background()))
	require.Equal(t, RequestIDPrefix(), cache.cleanupPrefix)
}

func TestAcquireProviderSlot_Success(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 1, 5)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
}

func TestAcquireProviderSlot_Failure(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: false}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 1, 5)
	require.NoError(t, err)
	require.False(t, result.Acquired)
	require.Nil(t, result.ReleaseFunc)
}

func TestAcquireProviderSlot_UnlimitedConcurrency(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	for _, maxConcurrency := range []int{0, -1} {
		result, err := svc.AcquireProviderSlot(context.Background(), 1, maxConcurrency)
		require.NoError(t, err)
		require.True(t, result.Acquired, "maxConcurrency=%d 应无限制通过", maxConcurrency)
		require.NotNil(t, result.ReleaseFunc, "ReleaseFunc 应为 no-op 函数")
	}
}

func TestAcquireProviderSlot_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 1, 5)
	require.Error(t, err)
	require.Nil(t, result)
}

func TestAcquireProviderSlot_ReleaseDecrements(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 42, 5)
	require.NoError(t, err)
	require.True(t, result.Acquired)

	// Releasing the result returns the acquired slot.
	result.ReleaseFunc()

	require.Len(t, cache.releasedProviderIDs, 1)
	require.Equal(t, int64(42), cache.releasedProviderIDs[0])
	require.Len(t, cache.releasedRequestIDs, 1)
	require.NotEmpty(t, cache.releasedRequestIDs[0], "requestID 不应为空")
}

func TestAcquireUserSlot_IndependentFromProvider(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	// User slots are acquired independently of provider slots.
	result, err := svc.AcquireUserSlot(context.Background(), 100, 3)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
}

func TestAcquireUserSlot_UnlimitedConcurrency(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	result, err := svc.AcquireUserSlot(context.Background(), 1, 0)
	require.NoError(t, err)
	require.True(t, result.Acquired)
}

func TestTrackAPIKeySlot_ReleaseDecrements(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{}
	svc := NewConcurrencyService(cache)

	release := svc.TrackAPIKeySlot(context.Background(), 88)
	require.NotNil(t, release)
	require.Equal(t, []int64{88}, cache.trackedAPIKeyIDs)
	require.Len(t, cache.trackedAPIKeyRequestIDs, 1)
	require.NotEmpty(t, cache.trackedAPIKeyRequestIDs[0])

	release()

	require.Equal(t, []int64{88}, cache.releasedAPIKeyIDs)
	require.Equal(t, cache.trackedAPIKeyRequestIDs, cache.releasedAPIKeyRequestIDs)
}

func TestTrackAPIKeySlot_FailOpen(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{apiKeyTrackErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)

	release := svc.TrackAPIKeySlot(context.Background(), 88)
	require.NotNil(t, release)
	require.Equal(t, []int64{88}, cache.trackedAPIKeyIDs)

	require.NotPanics(t, release)
	require.Empty(t, cache.releasedAPIKeyIDs)
}

func TestGetAPIKeyConcurrencyBatch_Fallbacks(t *testing.T) {
	t.Run("nil cache returns zeroes", func(t *testing.T) {
		svc := &ConcurrencyService{cache: nil}

		counts, err := svc.GetAPIKeyConcurrencyBatch(context.Background(), []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, map[int64]int{1: 0, 2: 0}, counts)
	})

	t.Run("redis error returns zeroes", func(t *testing.T) {
		cache := &stubConcurrencyCacheForTest{apiKeyConcurrencyErr: errors.New("redis down")}
		svc := NewConcurrencyService(cache)

		counts, err := svc.GetAPIKeyConcurrencyBatch(context.Background(), []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, map[int64]int{1: 0, 2: 0}, counts)
	})

	t.Run("success returns counts", func(t *testing.T) {
		cache := &stubConcurrencyCacheForTest{apiKeyConcurrency: map[int64]int{1: 3, 2: 0}}
		svc := NewConcurrencyService(cache)

		counts, err := svc.GetAPIKeyConcurrencyBatch(context.Background(), []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, map[int64]int{1: 3, 2: 0}, counts)
	})
}

func TestAcquireOpenAIWSIngressLease(t *testing.T) {
	t.Run("zero value release is safe", func(t *testing.T) {
		var lease OpenAIWSIngressLease
		require.NotPanics(t, lease.Release)
	})

	t.Run("disabled", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{}
		// With limiting disabled, a nil context still permits the request.
		var requestContext context.Context
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(requestContext, 1, 0)
		require.NoError(t, err)
		require.True(t, acquired)
		require.Nil(t, lease)
		require.Zero(t, cache.acquireIngressCalls)
	})

	t.Run("unsupported cache fails closed", func(t *testing.T) {
		lease, acquired, err := NewConcurrencyService(&stubConcurrencyCacheForTest{}).AcquireOpenAIWSIngressLease(context.Background(), 1, 1)
		require.Error(t, err)
		require.False(t, acquired)
		require.Nil(t, lease)
	})

	t.Run("capacity rejected", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{acquireIngressResult: false}
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(context.Background(), 1, 1)
		require.NoError(t, err)
		require.False(t, acquired)
		require.Nil(t, lease)
	})

	t.Run("release returns capacity", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{acquireIngressResult: true, refreshIngressResult: true}
		// A nil context still produces a lease that tolerates repeated release.
		var requestContext context.Context
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(requestContext, 1, 1)
		require.NoError(t, err)
		require.True(t, acquired)
		require.NotNil(t, lease)
		lease.Release()
		lease.Release()
		require.Equal(t, 1, cache.releaseIngressCalls)
	})
}

func TestOpenAIWSIngressLeaseRefreshLoss(t *testing.T) {
	t.Run("missing lease is lost immediately", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{refreshIngressResult: false}
		lease := &OpenAIWSIngressLease{cache: cache, apiKeyID: 1, leaseID: "missing"}
		_, lost := lease.refresh(time.Now())
		require.True(t, lost)
		require.Equal(t, 1, cache.refreshIngressCalls)
	})

	t.Run("persistent redis errors lose lease after ttl", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{refreshIngressErr: errors.New("redis unavailable")}
		lease := &OpenAIWSIngressLease{cache: cache, apiKeyID: 1, leaseID: "unconfirmed"}
		_, lost := lease.refresh(time.Now().Add(-openAIWSIngressLeaseTTL))
		require.True(t, lost)
		require.Equal(t, 1, cache.refreshIngressCalls)
	})
}

func TestOpenAIWSIngressLeaseReleaseWaitsForInFlightRefresh(t *testing.T) {
	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	cache := &ingressLeaseCacheForTest{
		refreshIngressFn: func(context.Context, int64, string) (bool, error) {
			close(refreshStarted)
			<-allowRefresh
			return true, nil
		},
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	lease := &OpenAIWSIngressLease{
		ctx:         ctx,
		cancel:      cancel,
		cache:       cache,
		apiKeyID:    1,
		leaseID:     "in-flight-refresh",
		stopCh:      make(chan struct{}),
		refreshDone: make(chan struct{}),
	}
	go func() {
		defer close(lease.refreshDone)
		_, _ = lease.refresh(time.Now())
	}()
	<-refreshStarted

	released := make(chan struct{})
	go func() {
		lease.Release()
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("release returned before the in-flight refresh completed")
	case <-time.After(20 * time.Millisecond):
	}
	require.Zero(t, cache.releaseIngressCalls)

	close(allowRefresh)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("release did not complete after the refresh returned")
	}
	require.Equal(t, 1, cache.releaseIngressCalls)
}

func TestGenerateRequestID_UsesStablePrefixAndMonotonicCounter(t *testing.T) {
	id1 := GenerateRequestID()
	id2 := GenerateRequestID()
	require.NotEmpty(t, id1)
	require.NotEmpty(t, id2)

	p1 := strings.Split(id1, "-")
	p2 := strings.Split(id2, "-")
	require.Len(t, p1, 2)
	require.Len(t, p2, 2)
	require.Equal(t, p1[0], p2[0], "同一进程前缀应保持一致")

	n1, err := strconv.ParseUint(p1[1], 36, 64)
	require.NoError(t, err)
	n2, err := strconv.ParseUint(p2[1], 36, 64)
	require.NoError(t, err)
	require.Equal(t, n1+1, n2, "计数器应单调递增")
}

func TestGetProvidersLoadBatch_ReturnsCorrectData(t *testing.T) {
	expected := map[int64]*ProviderLoadInfo{
		1: {ProviderID: 1, CurrentConcurrency: 3, WaitingCount: 0, LoadRate: 60},
		2: {ProviderID: 2, CurrentConcurrency: 5, WaitingCount: 2, LoadRate: 100},
	}
	cache := &stubConcurrencyCacheForTest{loadBatch: expected}
	svc := NewConcurrencyService(cache)

	providers := []ProviderWithConcurrency{
		{ID: 1, MaxConcurrency: 5},
		{ID: 2, MaxConcurrency: 5},
	}
	result, err := svc.GetProvidersLoadBatch(context.Background(), providers)
	require.NoError(t, err)
	require.Equal(t, expected, result)
}

func TestGetProvidersLoadBatch_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	result, err := svc.GetProvidersLoadBatch(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestGetProvidersLoadBatch_UsesShortTTLCache(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{
		loadBatch: map[int64]*ProviderLoadInfo{
			1: {ProviderID: 1, CurrentConcurrency: 1, LoadRate: 20},
		},
	}
	svc := NewConcurrencyService(cache)
	svc.SetProviderLoadBatchCacheTTL(time.Second)

	providers := []ProviderWithConcurrency{{ID: 1, MaxConcurrency: 5}}
	first, err := svc.GetProvidersLoadBatch(context.Background(), providers)
	require.NoError(t, err)
	require.Equal(t, 1, first[int64(1)].CurrentConcurrency)

	cache.loadBatch[1] = &ProviderLoadInfo{ProviderID: 1, CurrentConcurrency: 4, LoadRate: 80}
	second, err := svc.GetProvidersLoadBatch(context.Background(), providers)
	require.NoError(t, err)
	require.Equal(t, 1, second[int64(1)].CurrentConcurrency)
	require.Equal(t, int64(1), cache.loadBatchCalls.Load())
}

func TestGetProvidersLoadBatchFresh_BypassesShortTTLCache(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{
		loadBatch: map[int64]*ProviderLoadInfo{
			1: {ProviderID: 1, CurrentConcurrency: 1, LoadRate: 20},
		},
	}
	svc := NewConcurrencyService(cache)
	svc.SetProviderLoadBatchCacheTTL(time.Second)

	providers := []ProviderWithConcurrency{{ID: 1, MaxConcurrency: 5}}
	_, err := svc.GetProvidersLoadBatch(context.Background(), providers)
	require.NoError(t, err)

	cache.loadBatch[1] = &ProviderLoadInfo{ProviderID: 1, CurrentConcurrency: 4, LoadRate: 80}
	fresh, err := svc.GetProvidersLoadBatchFresh(context.Background(), providers)
	require.NoError(t, err)
	require.Equal(t, 4, fresh[int64(1)].CurrentConcurrency)
	require.Equal(t, int64(2), cache.loadBatchCalls.Load())
}

func TestIncrementWaitCount_Success(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)
}

func TestIncrementWaitCount_QueueFull(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: false}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.False(t, allowed.Allowed)
}

func TestIncrementWaitCount_FailOpen(t *testing.T) {
	// Redis errors allow the request through.
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis timeout")}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err, "Redis 错误不应传播")
	require.True(t, allowed.Allowed, "Redis 错误时应 fail-open")
}

func TestIncrementWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed.Allowed, "nil cache 应 fail-open")
}

func TestCalculateMaxWait(t *testing.T) {
	tests := []struct {
		concurrency int
		expected    int
	}{
		{5, 25},  // 5 + 20
		{1, 21},  // 1 + 20
		{0, 21},  // min(1) + 20
		{-1, 21}, // min(1) + 20
		{10, 30}, // 10 + 20
	}
	for _, tt := range tests {
		result := CalculateMaxWait(tt.concurrency)
		require.Equal(t, tt.expected, result, "CalculateMaxWait(%d)", tt.concurrency)
	}
}

func TestGetProviderWaitingCount(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitCount: 5}
	svc := NewConcurrencyService(cache)

	count, err := svc.GetProviderWaitingCount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 5, count)
}

func TestGetProviderWaitingCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	count, err := svc.GetProviderWaitingCount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestGetProviderConcurrencyBatch(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{concurrency: 3}
	svc := NewConcurrencyService(cache)

	result, err := svc.GetProviderConcurrencyBatch(context.Background(), []int64{1, 2, 3})
	require.NoError(t, err)
	require.Len(t, result, 3)
	for _, id := range []int64{1, 2, 3} {
		require.Equal(t, 3, result[id])
	}
}

func TestIncrementProviderWaitCount_FailOpen(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis error")}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterProviderWait(context.Background(), 1, 10)
	require.NoError(t, err, "Redis 错误不应传播")
	require.True(t, allowed.Allowed, "Redis 错误时应 fail-open")
}

func TestIncrementProviderWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	allowed, err := svc.EnterProviderWait(context.Background(), 1, 10)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)
}

// TestDecrementWaitCount_NilCache exercises release with no cache.
func TestDecrementWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	wait, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestDecrementWaitCount_CacheError exercises release after admission.
func TestDecrementWaitCount_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)
	// Release performs cleanup with its own context.
	wait, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestDecrementProviderWaitCount_NilCache exercises provider release with no cache.
func TestDecrementProviderWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	wait, err := svc.EnterProviderWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestDecrementProviderWaitCount_CacheError exercises provider release after admission.
func TestDecrementProviderWaitCount_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)
	wait, err := svc.EnterProviderWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestWaitingQueueFlow_IncrementThenDecrement exercises user wait admission and release.
func TestWaitingQueueFlow_IncrementThenDecrement(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)

	allowed.Release()
}

// TestWaitingQueueFlow_ProviderLevel exercises provider wait admission and release.
func TestWaitingQueueFlow_ProviderLevel(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterProviderWait(context.Background(), 42, 10)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)

	allowed.Release()
}

// TestWaitingQueueFull_Returns429Signal checks that a full queue denies admission.
func TestWaitingQueueFull_Returns429Signal(t *testing.T) {
	// A false waitAllowed result represents a full queue.
	cache := &stubConcurrencyCacheForTest{waitAllowed: false}
	svc := NewConcurrencyService(cache)

	// The user queue denies admission.
	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.False(t, allowed.Allowed, "等待队列满时应返回 false（调用方根据此返回 429）")

	// The provider queue denies admission.
	allowed, err = svc.EnterProviderWait(context.Background(), 1, 10)
	require.NoError(t, err)
	require.False(t, allowed.Allowed, "提供商等待队列满时应返回 false")
}

// TestWaitingQueue_FailOpen_OnCacheError checks admission during Redis failures.
func TestWaitingQueue_FailOpen_OnCacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis connection refused")}
	svc := NewConcurrencyService(cache)

	// User admission proceeds after a Redis error.
	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err, "Redis 错误不应向调用方传播")
	require.True(t, allowed.Allowed, "Redis 故障时应 fail-open 放行")

	// Provider admission also proceeds after a Redis error.
	allowed, err = svc.EnterProviderWait(context.Background(), 1, 10)
	require.NoError(t, err, "Redis 错误不应向调用方传播")
	require.True(t, allowed.Allowed, "Redis 故障时应 fail-open 放行")
}

// TestCalculateMaxWait_Scenarios checks the queue size for each concurrency limit.
func TestCalculateMaxWait_Scenarios(t *testing.T) {
	tests := []struct {
		concurrency int
		expected    int
	}{
		{5, 25},    // 5 + 20
		{10, 30},   // 10 + 20
		{1, 21},    // 1 + 20
		{0, 21},    // min(1) + 20
		{-1, 21},   // min(1) + 20
		{-10, 21},  // min(1) + 20
		{100, 120}, // 100 + 20
	}
	for _, tt := range tests {
		result := CalculateMaxWait(tt.concurrency)
		require.Equal(t, tt.expected, result, "CalculateMaxWait(%d)", tt.concurrency)
	}
}

func TestRequestLeaseStopWaitsForActualRelease(t *testing.T) {
	cache := &runtimeSlotCache{acquired: true, releaseStarted: make(chan struct{}), releaseContinue: make(chan struct{})}
	core := NewConcurrencyService(cache)
	slot, err := core.AcquireProviderSlot(context.Background(), 7, 1)
	require.NoError(t, err)
	stopped := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { stopped <- core.StopContext(ctx) }()
	<-core.runtime.Context().Done()
	select {
	case err := <-stopped:
		t.Fatalf("释放前停止已返回：%v", err)
	default:
	}
	released := make(chan struct{})
	go func() { slot.ReleaseFunc(); close(released) }()
	<-cache.releaseStarted
	select {
	case err := <-stopped:
		t.Fatalf("Redis 释放未完成即停止：%v", err)
	default:
	}
	close(cache.releaseContinue)
	<-released
	require.NoError(t, <-stopped)
	slot.ReleaseFunc()
	require.Equal(t, int64(1), cache.releases.Load())
	_, err = core.AcquireProviderSlot(context.Background(), 7, 1)
	require.ErrorIs(t, err, ErrRuntimeStopped)
}

func TestRequestLeaseStopTimeoutRetainsFailure(t *testing.T) {
	cache := &runtimeSlotCache{acquired: true}
	core := NewConcurrencyService(cache)
	slot, err := core.AcquireProviderSlot(context.Background(), 9, 1)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	stopErr := core.StopContext(ctx)
	require.ErrorIs(t, stopErr, context.DeadlineExceeded)
	require.ErrorContains(t, stopErr, "provider-slot:9")
	slot.ReleaseFunc()
	require.Equal(t, stopErr, core.StopContext(context.Background()))
}

// TestUnlimitedSlotPreservesCallerCancellation checks immediate admission with a canceled context when limits are disabled.
func TestUnlimitedSlotPreservesCallerCancellation(t *testing.T) {
	for _, user := range []bool{false, true} {
		core := NewConcurrencyService(nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var slot *AcquireResult
		var err error
		if user {
			slot, err = core.AcquireUserSlot(ctx, 4, 0)
		} else {
			slot, err = core.AcquireProviderSlot(ctx, 4, 0)
		}
		require.NoError(t, err)
		require.True(t, slot.Acquired)
		slot.ReleaseFunc()
		require.NoError(t, core.StopContext(context.Background()))
	}
}

func (c *slotCleanupCache) CleanupExpiredProviderSlotKeys(context.Context) error {
	c.calls.Add(1)
	return nil
}

func (c *ingressLeaseCacheForTest) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	c.acquireIngressCalls++
	if c.acquireIngressFn != nil {
		return c.acquireIngressFn(ctx, apiKeyID, maxConnections, leaseID)
	}
	return c.acquireIngressResult, c.acquireIngressErr
}

func (c *ingressLeaseCacheForTest) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	c.refreshIngressCalls++
	if c.refreshIngressFn != nil {
		return c.refreshIngressFn(ctx, apiKeyID, leaseID)
	}
	return c.refreshIngressResult, c.refreshIngressErr
}

func (c *ingressLeaseCacheForTest) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	c.releaseIngressCalls++
	if c.releaseIngressFn != nil {
		return c.releaseIngressFn(ctx, apiKeyID, leaseID)
	}
	return c.releaseIngressErr
}

func (c *stubConcurrencyCacheForTest) AcquireProviderSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return c.acquireResult, c.acquireErr
}

func (c *stubConcurrencyCacheForTest) ReleaseProviderSlot(_ context.Context, providerID int64, requestID string) error {
	c.releasedProviderIDs = append(c.releasedProviderIDs, providerID)
	c.releasedRequestIDs = append(c.releasedRequestIDs, requestID)
	return c.releaseErr
}

func (c *stubConcurrencyCacheForTest) GetProviderConcurrency(_ context.Context, _ int64) (int, error) {
	return c.concurrency, c.concurrencyErr
}

func (c *stubConcurrencyCacheForTest) GetProviderConcurrencyBatch(_ context.Context, providerIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(providerIDs))
	for _, providerID := range providerIDs {
		if c.concurrencyErr != nil {
			return nil, c.concurrencyErr
		}
		result[providerID] = c.concurrency
	}
	return result, nil
}

func (c *stubConcurrencyCacheForTest) IncrementProviderWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return c.waitAllowed, c.waitErr
}

func (c *stubConcurrencyCacheForTest) DecrementProviderWaitCount(_ context.Context, _ int64) error {
	return nil
}

func (c *stubConcurrencyCacheForTest) GetProviderWaitingCount(_ context.Context, _ int64) (int, error) {
	return c.waitCount, c.waitCountErr
}

func (c *stubConcurrencyCacheForTest) AcquireUserSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return c.acquireResult, c.acquireErr
}

func (c *stubConcurrencyCacheForTest) ReleaseUserSlot(_ context.Context, _ int64, _ string) error {
	return c.releaseErr
}

func (c *stubConcurrencyCacheForTest) GetUserConcurrency(_ context.Context, _ int64) (int, error) {
	return c.concurrency, c.concurrencyErr
}

func (c *stubConcurrencyCacheForTest) TrackAPIKeySlot(_ context.Context, apiKeyID int64, requestID string) error {
	c.trackedAPIKeyIDs = append(c.trackedAPIKeyIDs, apiKeyID)
	c.trackedAPIKeyRequestIDs = append(c.trackedAPIKeyRequestIDs, requestID)
	return c.apiKeyTrackErr
}

func (c *stubConcurrencyCacheForTest) ReleaseAPIKeySlot(_ context.Context, apiKeyID int64, requestID string) error {
	c.releasedAPIKeyIDs = append(c.releasedAPIKeyIDs, apiKeyID)
	c.releasedAPIKeyRequestIDs = append(c.releasedAPIKeyRequestIDs, requestID)
	return c.apiKeyReleaseErr
}

func (c *stubConcurrencyCacheForTest) GetAPIKeyConcurrencyBatch(_ context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	if c.apiKeyConcurrencyErr != nil {
		return nil, c.apiKeyConcurrencyErr
	}
	result := make(map[int64]int, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = c.apiKeyConcurrency[apiKeyID]
	}
	return result, nil
}

func (c *stubConcurrencyCacheForTest) IncrementWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return c.waitAllowed, c.waitErr
}

func (c *stubConcurrencyCacheForTest) DecrementWaitCount(_ context.Context, _ int64) error {
	return nil
}

func (c *stubConcurrencyCacheForTest) GetProvidersLoadBatch(_ context.Context, _ []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	c.loadBatchCalls.Add(1)
	return c.loadBatch, c.loadBatchErr
}

func (c *stubConcurrencyCacheForTest) GetUsersLoadBatch(_ context.Context, _ []UserWithConcurrency) (map[int64]*UserLoadInfo, error) {
	return c.usersLoadBatch, c.usersLoadErr
}

func (c *stubConcurrencyCacheForTest) CleanupExpiredProviderSlots(_ context.Context, _ int64) error {
	return c.cleanupErr
}

func (c *stubConcurrencyCacheForTest) CleanupExpiredProviderSlotKeys(_ context.Context) error {
	return c.cleanupErr
}

func (c *stubConcurrencyCacheForTest) CleanupStaleProcessSlots(_ context.Context, _ string) error {
	return c.cleanupErr
}

func (c *trackingConcurrencyCache) CleanupStaleProcessSlots(_ context.Context, prefix string) error {
	c.cleanupPrefix = prefix
	return c.cleanupErr
}
