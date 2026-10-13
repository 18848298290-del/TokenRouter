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

// Interface assertions keep the ordinary concurrency and ingress fixtures
// assignable to the capabilities exercised by these service tests.
var (
	_ ConcurrencyCache          = (*stubConcurrencyCacheForTest)(nil)
	_ OpenAIWSIngressLeaseCache = (*ingressLeaseCacheForTest)(nil)
)

// slotCleanupCache counts indexed cleanup calls while delegating unused cache
// methods through an embedded interface.
type slotCleanupCache struct {
	ConcurrencyCache              // unused cache method delegation
	calls            atomic.Int64 // indexed cleanup invocation count
}

// stubConcurrencyCacheForTest supplies configured service results and records
// selected cache interactions. Tests configure borrowed maps and scalar
// results before use; most call records assume sequential operations while
// load-fetch counts use an atomic field.
type stubConcurrencyCacheForTest struct {
	acquireResult  bool  // configured ordinary slot admission
	acquireErr     error // configured slot acquisition failure
	releaseErr     error // configured ordinary release failure
	concurrency    int   // configured active slot count
	concurrencyErr error // configured slot observation failure
	waitAllowed    bool  // configured waiting admission result
	waitErr        error // configured wait ownership failure
	waitCount      int   // configured provider waiting observation
	waitCountErr   error // configured waiting observation failure
	// loadBatch and usersLoadBatch retain supplied map and record references.
	// GetProvidersLoadBatch and GetUsersLoadBatch return them directly, allowing
	// tests to distinguish service cache copies from later fixture replacement.
	loadBatch        map[int64]*ProviderLoadInfo // borrowed provider load results
	loadBatchErr     error                       // configured provider observation failure
	usersLoadBatch   map[int64]*UserLoadInfo     // borrowed user load results
	usersLoadErr     error                       // configured user observation failure
	cleanupErr       error                       // configured expiry cleanup failure
	apiKeyTrackErr   error                       // configured key tracking failure
	apiKeyReleaseErr error                       // configured key cleanup failure
	// apiKeyConcurrency retains test-supplied scalar counts. Tests keep this map
	// stable while the fixture copies requested entries into each returned map.
	apiKeyConcurrency    map[int64]int // borrowed key count results
	apiKeyConcurrencyErr error         // configured key observation failure

	// releasedProviderIDs and releasedRequestIDs record matching release calls in
	// the same positions. Each pair retains the owner and exact member identity
	// supplied to ReleaseProviderSlot.
	releasedProviderIDs []int64      // recorded released provider identifiers
	releasedRequestIDs  []string     // recorded released member identifiers
	loadBatchCalls      atomic.Int64 // provider batch read count
	// trackedAPIKeyIDs and trackedAPIKeyRequestIDs pair each tracking call.
	// releasedAPIKeyIDs and releasedAPIKeyRequestIDs pair cleanup calls, so tests
	// can compare the acquired and removed statistics identities.
	trackedAPIKeyIDs         []int64  // recorded tracked key identifiers
	trackedAPIKeyRequestIDs  []string // recorded tracked member identifiers
	releasedAPIKeyIDs        []int64  // recorded released key identifiers
	releasedAPIKeyRequestIDs []string // recorded released key members
}

// ingressLeaseCacheForTest adds configurable ingress callbacks and invocation
// counts to the ordinary cache fixture. Hooks are configured before
// asynchronous work, and test barriers coordinate reads of its unsynchronized
// counters.
type ingressLeaseCacheForTest struct {
	stubConcurrencyCacheForTest // inherited ordinary cache behavior
	// acquireIngressResult, acquireIngressErr, and acquireIngressFn describe
	// acquisition together. A nonnil hook overrides the configured result and
	// error after acquireIngressCalls records invocation.
	acquireIngressResult bool  // configured ingress admission result
	acquireIngressErr    error // configured ingress acquisition failure
	// optional ingress acquisition hook
	acquireIngressFn func(context.Context, int64, int, string) (bool, error)
	// refreshIngressResult, refreshIngressErr, and refreshIngressFn describe
	// renewal together. A nonnil hook overrides the scalar response after
	// refreshIngressCalls records invocation, including blocking renewal
	// scenarios.
	refreshIngressResult bool  // configured ingress renewal ownership
	refreshIngressErr    error // configured ingress renewal failure
	// optional ingress renewal hook
	refreshIngressFn func(context.Context, int64, string) (bool, error)
	// releaseIngressErr and releaseIngressFn supply cleanup outcomes, with a
	// nonnil hook taking precedence. releaseIngressCalls increments before the
	// hook runs so barriers can observe entered cleanup.
	releaseIngressErr   error                                      // configured ingress cleanup failure
	releaseIngressFn    func(context.Context, int64, string) error // optional ingress cleanup hook
	acquireIngressCalls int                                        // ingress acquisition invocation count
	refreshIngressCalls int                                        // ingress renewal invocation count
	releaseIngressCalls int                                        // ingress cleanup invocation count
}

// trackingConcurrencyCache records the startup prefix forwarded by the
// concurrency service.
type trackingConcurrencyCache struct {
	stubConcurrencyCacheForTest        // inherited ordinary cache behavior
	cleanupPrefix               string // recorded active process prefix
}

// TestStartSlotCleanupWorker_UsesCacheWideCleanupWithoutProviderRepo starts a
// long-interval cleanup worker with a cache-wide recording fixture. It waits
// for an immediate cleanup call, protecting startup maintenance that uses the
// cache rather than provider discovery.
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

// TestCleanupStaleProcessSlots_NilCache uses a service without a cache and
// invokes startup cleanup. It requires no error, protecting the
// optional-adapter bypass before any stale-member sweep can run.
func TestCleanupStaleProcessSlots_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	require.NoError(t, svc.CleanupStaleProcessSlots(context.Background()))
}

// TestCleanupStaleProcessSlots_DelegatesPrefix invokes startup cleanup through
// a prefix-recording adapter. It checks that the service forwards its actual
// process prefix, keeping cleanup ownership consistent with generated member
// IDs.
func TestCleanupStaleProcessSlots_DelegatesPrefix(t *testing.T) {
	cache := &trackingConcurrencyCache{}
	svc := NewConcurrencyService(cache)
	require.NoError(t, svc.CleanupStaleProcessSlots(context.Background()))
	require.Equal(t, RequestIDPrefix(), cache.cleanupPrefix)
}

// TestAcquireProviderSlot_Success uses a cache reporting successful provider
// admission. It checks the acquired flag and nonnil cleanup handle, protecting
// the result shape returned to an execution caller.
func TestAcquireProviderSlot_Success(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 1, 5)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
}

// TestAcquireProviderSlot_Failure uses a cache denying provider admission
// without an operation error. It requires a denied result and nil cleanup
// callback because no confirmed slot ownership was transferred.
func TestAcquireProviderSlot_Failure(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: false}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 1, 5)
	require.NoError(t, err)
	require.False(t, result.Acquired)
	require.Nil(t, result.ReleaseFunc)
}

// TestAcquireProviderSlot_UnlimitedConcurrency requests provider admission
// with zero and negative limits. Both results must succeed and expose a
// release handle, protecting disabled physical counting through the public
// service path.
func TestAcquireProviderSlot_UnlimitedConcurrency(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	for _, maxConcurrency := range []int{0, -1} {
		result, err := svc.AcquireProviderSlot(context.Background(), 1, maxConcurrency)
		require.NoError(t, err)
		require.True(t, result.Acquired, "maxConcurrency=%d 应无限制通过", maxConcurrency)
		require.NotNil(t, result.ReleaseFunc, "ReleaseFunc 应为 no-op 函数")
	}
}

// TestAcquireProviderSlot_CacheError fails provider acquisition through the
// cache fixture. It requires an error and no result, protecting propagation of
// an unconfirmed slot operation to its caller.
func TestAcquireProviderSlot_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireProviderSlot(context.Background(), 1, 5)
	require.Error(t, err)
	require.Nil(t, result)
}

// TestAcquireProviderSlot_ReleaseDecrements acquires a provider slot and
// invokes its returned cleanup. It checks one release with the same provider
// ID and a nonempty member ID, protecting owned-member identification.
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

// TestAcquireUserSlot_IndependentFromProvider requests a user slot from a
// successful fixture. It checks admitted output and a release handle; the
// fixture supplies no assertion of provider-call independence in this
// scenario.
func TestAcquireUserSlot_IndependentFromProvider(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireUserSlot(context.Background(), 100, 3)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
}

// TestAcquireUserSlot_UnlimitedConcurrency requests user admission with a zero
// limit. It requires a successful acquired result, protecting the public user
// path that bypasses physical cache counting.
func TestAcquireUserSlot_UnlimitedConcurrency(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	result, err := svc.AcquireUserSlot(context.Background(), 1, 0)
	require.NoError(t, err)
	require.True(t, result.Acquired)
}

// TestTrackAPIKeySlot_ReleaseDecrements tracks statistics for one key and
// releases the returned handle. It checks key IDs and matching member IDs
// across tracking and removal, protecting identity-preserving statistics
// cleanup.
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

// TestTrackAPIKeySlot_FailOpen fails statistics tracking through the cache
// adapter. It requires a callable no-op release and no recorded key removal,
// protecting requests that proceed with unconfirmed statistics ownership.
func TestTrackAPIKeySlot_FailOpen(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{apiKeyTrackErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)

	release := svc.TrackAPIKeySlot(context.Background(), 88)
	require.NotNil(t, release)
	require.Equal(t, []int64{88}, cache.trackedAPIKeyIDs)

	require.NotPanics(t, release)
	require.Empty(t, cache.releasedAPIKeyIDs)
}

// TestGetAPIKeyConcurrencyBatch_Fallbacks compares absent cache, failed
// adapter read, and successful key statistics. It checks zero-map recovery and
// supplied counts, protecting observational results that do not reject
// requests on cache failure.
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

// TestAcquireOpenAIWSIngressLease covers zero-value cleanup, disabled
// admission, unsupported cache, capacity denial, and repeated release after
// success. It checks result shapes, skipped acquisition when disabled, and
// exactly one recorded member cleanup.
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

// TestOpenAIWSIngressLeaseRefreshLoss compares missing renewal membership with
// errors lasting one full TTL. It requires a loss decision and one renewal
// call for each case, protecting immediate versus unconfirmed ownership
// handling.
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

// TestOpenAIWSIngressLeaseReleaseWaitsForInFlightRefresh blocks a renewal call
// and dispatches release concurrently. It observes no release return or member
// cleanup during a short window, then permits renewal completion and checks
// exactly one cleanup call.
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
	// The test renewal announces entry before blocking. Keep its completion
	// channel open until that callback returns so concurrent Release must wait
	// before member removal.
	go func() {
		defer close(lease.refreshDone)
		_, _ = lease.refresh(time.Now())
	}()
	<-refreshStarted

	released := make(chan struct{})
	// Dispatch Release while renewal remains blocked. Sample its return signal
	// during the blocked window, then join that call after refresh completes;
	// refreshStarted supplies the separate renewal-entry signal.
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

// TestGenerateRequestID_UsesStablePrefixAndMonotonicCounter generates two
// consecutive internal member IDs. It checks their common prefix, base36
// sequence form, and adjacent counter values, protecting process-local
// identity construction for these calls.
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

// TestGetProvidersLoadBatch_ReturnsCorrectData reads two provider observations
// through the service cache path. It compares returned scalar records with the
// supplied occupancy, queue, and percentage fixture values.
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

// TestGetProvidersLoadBatch_NilCache supplies both an absent cache and empty
// provider input. It requires a successful empty result, which exercises the
// empty-input path before any adapter lookup is needed.
func TestGetProvidersLoadBatch_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	result, err := svc.GetProvidersLoadBatch(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, result)
}

// TestGetProvidersLoadBatch_UsesShortTTLCache replaces the fixture's provider
// load after a cached read. It requires the earlier observation and one
// adapter call on the second read, protecting short-lived shared reuse.
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

// TestGetProvidersLoadBatchFresh_BypassesShortTTLCache changes fixture load
// after a cached read and requests fresh observations. It requires the updated
// count and two adapter calls, protecting explicit cache bypass after
// contention.
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

// TestIncrementWaitCount_Success uses a cache permitting user wait admission.
// It checks successful allowed output, protecting service admission when the
// cache confirms a waiting position can be reserved.
func TestIncrementWaitCount_Success(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)
}

// TestIncrementWaitCount_QueueFull uses a cache denying user wait admission.
// It requires no operation error and false allowed output, keeping capacity
// rejection distinct from cache failure.
func TestIncrementWaitCount_QueueFull(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: false}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.False(t, allowed.Allowed)
}

// TestIncrementWaitCount_FailOpen fails user wait-count admission through the
// cache fixture. It requires successful allowed output despite that error,
// protecting the service's uncertain-ownership wait policy.
func TestIncrementWaitCount_FailOpen(t *testing.T) {
	// Redis errors allow the request through.
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis timeout")}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err, "Redis 错误不应传播")
	require.True(t, allowed.Allowed, "Redis 错误时应 fail-open")
}

// TestIncrementWaitCount_NilCache admits user waiting through a service with
// no cache. It requires successful allowed output, protecting the optional
// cache bypass for waiting policy.
func TestIncrementWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed.Allowed, "nil cache 应 fail-open")
}

// TestCalculateMaxWait checks positive, zero, and negative user limits against
// expected queue sizes. It protects the extra waiting allowance and the
// minimum-one interpretation for nonpositive inputs.
func TestCalculateMaxWait(t *testing.T) {
	tests := []struct {
		concurrency int // supplied user concurrency limit
		expected    int // expected bounded queue allowance
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

// TestGetProviderWaitingCount reads a configured provider queue observation
// through the service. It requires the supplied count and no error, protecting
// the adapter value passed to selection decisions.
func TestGetProviderWaitingCount(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitCount: 5}
	svc := NewConcurrencyService(cache)

	count, err := svc.GetProviderWaitingCount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 5, count)
}

// TestGetProviderWaitingCount_NilCache reads provider waiting pressure without
// a cache adapter. It requires zero and no error, protecting missing queue
// observation as an empty service result.
func TestGetProviderWaitingCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	count, err := svc.GetProviderWaitingCount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

// TestGetProviderConcurrencyBatch requests several provider IDs from a
// fixed-count adapter. It checks map size and the returned count for every ID,
// protecting service batch observation forwarding.
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

// TestIncrementProviderWaitCount_FailOpen fails provider waiting admission
// through the fixture. It requires allowed output and no propagated error,
// protecting uncertain cache ownership that permits the request to wait.
func TestIncrementProviderWaitCount_FailOpen(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis error")}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterProviderWait(context.Background(), 1, 10)
	require.NoError(t, err, "Redis 错误不应传播")
	require.True(t, allowed.Allowed, "Redis 错误时应 fail-open")
}

// TestIncrementProviderWaitCount_NilCache requests provider waiting with an
// absent cache. It requires successful allowed output, protecting optional
// queue storage during service wait admission.
func TestIncrementProviderWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	allowed, err := svc.EnterProviderWait(context.Background(), 1, 10)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)
}

// TestDecrementWaitCount_NilCache admits user waiting without a cache and
// releases the result. It checks admission succeeds and completes cleanup
// safely when no confirmed remote count exists.
func TestDecrementWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	wait, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestDecrementWaitCount_CacheError admits user waiting and invokes cleanup
// using a stub whose decrement succeeds. It checks admission and safe release;
// this scenario does not inject a decrement error despite its name.
func TestDecrementWaitCount_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)
	// Release performs cleanup with its own context.
	wait, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestDecrementProviderWaitCount_NilCache admits provider waiting without
// cache support and releases the returned result. It checks successful
// admission and completion of cleanup with no confirmed remote count.
func TestDecrementProviderWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	wait, err := svc.EnterProviderWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestDecrementProviderWaitCount_CacheError admits provider waiting and
// releases the result through a successful decrement stub. It checks safe
// cleanup after admission, with no decrement failure injected by this fixture.
func TestDecrementProviderWaitCount_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)
	wait, err := svc.EnterProviderWait(context.Background(), 1, 25)
	require.NoError(t, err)
	wait.Release()
}

// TestWaitingQueueFlow_IncrementThenDecrement admits a user wait and then
// invokes its cleanup. It requires allowed output and safe release, exercising
// the service's returned waiting owner.
func TestWaitingQueueFlow_IncrementThenDecrement(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterUserWait(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)

	allowed.Release()
}

// TestWaitingQueueFlow_ProviderLevel admits waiting for a provider through the
// configured fixture. It checks allowed output and invokes release, exercising
// adoption of provider wait cleanup by the service.
func TestWaitingQueueFlow_ProviderLevel(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.EnterProviderWait(context.Background(), 42, 10)
	require.NoError(t, err)
	require.True(t, allowed.Allowed)

	allowed.Release()
}

// TestWaitingQueueFull_Returns429Signal denies user and provider queue
// admission through the fixture. It requires false allowed output for both
// cases; HTTP status selection belongs to the caller rather than this test.
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

// TestWaitingQueue_FailOpen_OnCacheError fails both user and provider wait
// admission operations. It requires allowed output with no propagated error,
// protecting the same uncertain-ownership policy for both queue scopes.
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

// TestCalculateMaxWait_Scenarios checks small, large, and several nonpositive
// user concurrency inputs. Every expected size retains the fixed extra
// allowance and the minimum-one rule for nonpositive limits.
func TestCalculateMaxWait_Scenarios(t *testing.T) {
	tests := []struct {
		concurrency int // supplied user concurrency limit
		expected    int // expected bounded queue allowance
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

// TestRequestLeaseStopWaitsForActualRelease stops the service while a provider
// slot is owned, then blocks its cache cleanup. It checks that shutdown waits
// for completed removal, repeated release cleans once, and later acquisition
// is rejected.
func TestRequestLeaseStopWaitsForActualRelease(t *testing.T) {
	cache := &runtimeSlotCache{acquired: true, releaseStarted: make(chan struct{}), releaseContinue: make(chan struct{})}
	core := NewConcurrencyService(cache)
	slot, err := core.AcquireProviderSlot(context.Background(), 7, 1)
	require.NoError(t, err)
	stopped := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Start shutdown while slot ownership is retained. The runtime cancellation
	// signal proves Stop has entered before the test checks that owned capacity
	// still prevents its return.
	go func() { stopped <- core.StopContext(ctx) }()
	<-core.runtime.Context().Done()
	select {
	case err := <-stopped:
		t.Fatalf("释放前停止已返回：%v", err)
	default:
	}
	released := make(chan struct{})
	// Dispatch slot cleanup while shutdown is waiting, then observe cache release
	// entry. The separate return signal stays pending until the cache barrier
	// permits cleanup to finish.
	go func() { slot.ReleaseFunc(); close(released) }()
	<-cache.releaseStarted
	select {
	case err := <-stopped:
		t.Fatalf("Redis 释放未完成即停止：%v", err)
	default:
	}
	// Cache removal has entered but remains blocked. Permit its return only after
	// checking shutdown is still pending, then join cleanup before checking the
	// single release count.
	close(cache.releaseContinue)
	<-released
	require.NoError(t, <-stopped)
	slot.ReleaseFunc()
	require.Equal(t, int64(1), cache.releases.Load())
	_, err = core.AcquireProviderSlot(context.Background(), 7, 1)
	require.ErrorIs(t, err, ErrRuntimeStopped)
}

// TestRequestLeaseStopTimeoutRetainsFailure stops a service whose provider
// slot remains owned past the caller deadline. It requires the timeout cause
// and pending slot name, then confirms that later cleanup leaves the original
// stop failure retained.
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

// TestUnlimitedSlotPreservesCallerCancellation requests disabled-limit user
// and provider slots under already-canceled contexts. It checks successful
// admission, explicit release, and clean shutdown, protecting bypass behavior
// for these canceled inputs.
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

// CleanupExpiredProviderSlotKeys atomically records a cache-wide cleanup
// invocation for the worker test.
func (c *slotCleanupCache) CleanupExpiredProviderSlotKeys(context.Context) error {
	c.calls.Add(1)
	return nil
}

// AcquireOpenAIWSIngressLease records acquisition and invokes an optional
// configured hook. Without a hook it returns the fixture's scalar result and
// error, letting each test choose admission or failure independently.
func (c *ingressLeaseCacheForTest) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	c.acquireIngressCalls++
	if c.acquireIngressFn != nil {
		return c.acquireIngressFn(ctx, apiKeyID, maxConnections, leaseID)
	}
	return c.acquireIngressResult, c.acquireIngressErr
}

// RefreshOpenAIWSIngressLease records renewal and invokes its configured hook
// when present. Otherwise it returns stored ownership and error values,
// allowing the release-order test to block exactly one renewal call.
func (c *ingressLeaseCacheForTest) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	c.refreshIngressCalls++
	if c.refreshIngressFn != nil {
		return c.refreshIngressFn(ctx, apiKeyID, leaseID)
	}
	return c.refreshIngressResult, c.refreshIngressErr
}

// ReleaseOpenAIWSIngressLease records cleanup and delegates to its optional
// hook. An absent hook returns the configured release error; test barriers
// serialize counter observations rather than relying on internal fixture
// locking.
func (c *ingressLeaseCacheForTest) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	c.releaseIngressCalls++
	if c.releaseIngressFn != nil {
		return c.releaseIngressFn(ctx, apiKeyID, leaseID)
	}
	return c.releaseIngressErr
}

// AcquireProviderSlot returns the fixture's configured ordinary slot admission
// and operation error.
func (c *stubConcurrencyCacheForTest) AcquireProviderSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return c.acquireResult, c.acquireErr
}

// ReleaseProviderSlot records matching provider and member IDs before
// returning its configured error.
func (c *stubConcurrencyCacheForTest) ReleaseProviderSlot(_ context.Context, providerID int64, requestID string) error {
	c.releasedProviderIDs = append(c.releasedProviderIDs, providerID)
	c.releasedRequestIDs = append(c.releasedRequestIDs, requestID)
	return c.releaseErr
}

// GetProviderConcurrency returns the fixture's configured occupancy and
// observation error.
func (c *stubConcurrencyCacheForTest) GetProviderConcurrency(_ context.Context, _ int64) (int, error) {
	return c.concurrency, c.concurrencyErr
}

// GetProviderConcurrencyBatch fills each requested ID with configured
// occupancy or returns the configured read error. Empty input returns an empty
// map without reading that error because no ID is visited.
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

// IncrementProviderWaitCount returns the fixture's configured provider wait
// admission and uncertainty error.
func (c *stubConcurrencyCacheForTest) IncrementProviderWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return c.waitAllowed, c.waitErr
}

// DecrementProviderWaitCount accepts provider cleanup with no recorded state
// change or configured error.
func (c *stubConcurrencyCacheForTest) DecrementProviderWaitCount(_ context.Context, _ int64) error {
	return nil
}

// GetProviderWaitingCount returns the configured queue count and its
// observation error.
func (c *stubConcurrencyCacheForTest) GetProviderWaitingCount(_ context.Context, _ int64) (int, error) {
	return c.waitCount, c.waitCountErr
}

// AcquireUserSlot returns the fixture's configured ordinary admission result
// and error.
func (c *stubConcurrencyCacheForTest) AcquireUserSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return c.acquireResult, c.acquireErr
}

// ReleaseUserSlot returns the configured ordinary release error for user
// cleanup.
func (c *stubConcurrencyCacheForTest) ReleaseUserSlot(_ context.Context, _ int64, _ string) error {
	return c.releaseErr
}

// GetUserConcurrency returns the configured active count and observation error
// for user reads.
func (c *stubConcurrencyCacheForTest) GetUserConcurrency(_ context.Context, _ int64) (int, error) {
	return c.concurrency, c.concurrencyErr
}

// TrackAPIKeySlot records paired key and member IDs before returning
// configured tracking failure.
func (c *stubConcurrencyCacheForTest) TrackAPIKeySlot(_ context.Context, apiKeyID int64, requestID string) error {
	c.trackedAPIKeyIDs = append(c.trackedAPIKeyIDs, apiKeyID)
	c.trackedAPIKeyRequestIDs = append(c.trackedAPIKeyRequestIDs, requestID)
	return c.apiKeyTrackErr
}

// ReleaseAPIKeySlot records paired key and member IDs before returning
// configured cleanup failure.
func (c *stubConcurrencyCacheForTest) ReleaseAPIKeySlot(_ context.Context, apiKeyID int64, requestID string) error {
	c.releasedAPIKeyIDs = append(c.releasedAPIKeyIDs, apiKeyID)
	c.releasedAPIKeyRequestIDs = append(c.releasedAPIKeyRequestIDs, requestID)
	return c.apiKeyReleaseErr
}

// GetAPIKeyConcurrencyBatch returns configured failure before constructing any
// result, including for empty ID input. Otherwise it copies requested scalar
// counts from the borrowed fixture map into new result storage.
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

// IncrementWaitCount returns the fixture's configured user wait admission
// result and error.
func (c *stubConcurrencyCacheForTest) IncrementWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return c.waitAllowed, c.waitErr
}

// DecrementWaitCount accepts user wait cleanup with no configured decrement
// failure.
func (c *stubConcurrencyCacheForTest) DecrementWaitCount(_ context.Context, _ int64) error {
	return nil
}

// GetProvidersLoadBatch records an atomic fetch count and returns the borrowed
// provider map. Configured errors accompany that map, and later fixture
// replacement can test the service's independent cache copies.
func (c *stubConcurrencyCacheForTest) GetProvidersLoadBatch(_ context.Context, _ []ProviderWithConcurrency) (map[int64]*ProviderLoadInfo, error) {
	c.loadBatchCalls.Add(1)
	return c.loadBatch, c.loadBatchErr
}

// GetUsersLoadBatch returns the fixture's borrowed user observation map and
// configured error. Tests keep the map and its load records stable while the
// service consumes this result, since the fixture returns their original
// references.
func (c *stubConcurrencyCacheForTest) GetUsersLoadBatch(_ context.Context, _ []UserWithConcurrency) (map[int64]*UserLoadInfo, error) {
	return c.usersLoadBatch, c.usersLoadErr
}

// CleanupExpiredProviderSlots returns the configured maintenance error for the
// single-provider cleanup path.
func (c *stubConcurrencyCacheForTest) CleanupExpiredProviderSlots(_ context.Context, _ int64) error {
	return c.cleanupErr
}

// CleanupExpiredProviderSlotKeys returns configured maintenance failure for
// the cache-wide cleanup path.
func (c *stubConcurrencyCacheForTest) CleanupExpiredProviderSlotKeys(_ context.Context) error {
	return c.cleanupErr
}

// CleanupStaleProcessSlots returns configured maintenance failure for the
// startup cleanup path.
func (c *stubConcurrencyCacheForTest) CleanupStaleProcessSlots(_ context.Context, _ string) error {
	return c.cleanupErr
}

// CleanupStaleProcessSlots records the supplied process prefix before
// returning the configured cleanup error.
func (c *trackingConcurrencyCache) CleanupStaleProcessSlots(_ context.Context, prefix string) error {
	c.cleanupPrefix = prefix
	return c.cleanupErr
}
