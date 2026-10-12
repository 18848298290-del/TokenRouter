package billing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

type userGroupRateRepoHotpathStub struct {
	UserGroupRateRepository

	rate  *float64
	err   error
	wait  <-chan struct{}
	calls atomic.Int64
}

type userGroupRateResolverRepoStub struct {
	UserGroupRateRepository

	rate  *float64
	err   error
	calls int
}

func TestGetUserGroupRateMultiplier_UsesCacheAndSingleflight(t *testing.T) {
	resetGatewayRateStatsForTest()

	rate := 1.7
	unblock := make(chan struct{})
	repo := &userGroupRateRepoHotpathStub{
		rate: &rate,
		wait: unblock,
	}
	svc := NewGroupRateResolver(repo, gocache.New(time.Minute, time.Minute), 30*time.Second, nil, "service.gateway", nil)

	const concurrent = 12
	results := make([]float64, concurrent)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := range concurrent {
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = svc.Resolve(context.Background(), 101, 202, 1.2)
		}(i)
	}

	close(start)
	// Wait for every caller to record a cache miss before releasing the shared load.
	require.Eventually(t, func() bool {
		_, miss, _, _, _ := GroupRateCacheStats()
		return miss == int64(concurrent)
	}, 5*time.Second, time.Millisecond, "所有调用方必须在释放 loader 前完成 cache miss")
	close(unblock)
	wg.Wait()

	for _, got := range results {
		require.Equal(t, rate, got)
	}
	require.Equal(t, int64(1), repo.calls.Load())

	// A subsequent lookup uses the populated cache.
	got := svc.Resolve(context.Background(), 101, 202, 1.2)
	require.Equal(t, rate, got)
	require.Equal(t, int64(1), repo.calls.Load())

	hit, miss, load, sfShared, fallback := GroupRateCacheStats()
	require.GreaterOrEqual(t, hit, int64(1))
	require.Equal(t, int64(12), miss)
	require.Equal(t, int64(1), load)
	require.GreaterOrEqual(t, sfShared, int64(1))
	require.Equal(t, int64(0), fallback)
}

func TestGetUserGroupRateMultiplier_FallbackOnRepoError(t *testing.T) {
	resetGatewayRateStatsForTest()

	repo := &userGroupRateRepoHotpathStub{
		err: errors.New("db down"),
	}
	svc := NewGroupRateResolver(repo, gocache.New(time.Minute, time.Minute), 30*time.Second, nil, "service.gateway", nil)

	got := svc.Resolve(context.Background(), 101, 202, 1.25)
	require.Equal(t, 1.25, got)
	require.Equal(t, int64(1), repo.calls.Load())

	_, _, _, _, fallback := GroupRateCacheStats()
	require.Equal(t, int64(1), fallback)
}

func TestGetUserGroupRateMultiplier_CacheHitAndNilRepo(t *testing.T) {
	resetGatewayRateStatsForTest()

	repo := &userGroupRateRepoHotpathStub{
		err: errors.New("should not be called"),
	}
	svc := NewGroupRateResolver(repo, gocache.New(time.Minute, time.Minute), DefaultGroupRateCacheTTL, nil, "service.gateway", nil)
	key := "101:202"
	svc.cache.Set(key, 2.3, time.Minute)

	got := svc.Resolve(context.Background(), 101, 202, 1.1)
	require.Equal(t, 2.3, got)

	hit, miss, load, _, fallback := GroupRateCacheStats()
	require.Equal(t, int64(1), hit)
	require.Equal(t, int64(0), miss)
	require.Equal(t, int64(0), load)
	require.Equal(t, int64(0), fallback)
	require.Equal(t, int64(0), repo.calls.Load())

	// A cached value remains usable without a repository; invalid IDs return the group default.
	svc2 := NewGroupRateResolver(nil, gocache.New(time.Minute, time.Minute), DefaultGroupRateCacheTTL, nil, "service.gateway", nil)
	svc2.cache.Set(key, 1.9, time.Minute)
	require.Equal(t, 1.9, svc2.Resolve(context.Background(), 101, 202, 1.4))
	require.Equal(t, 1.4, svc2.Resolve(context.Background(), 0, 202, 1.4))
	svc2.cache.Delete(key)
	require.Equal(t, 1.4, svc2.Resolve(context.Background(), 101, 202, 1.4))
}

func TestNewUserGroupRateResolver_Defaults(t *testing.T) {
	resolver := NewGroupRateResolver(nil, nil, 0, nil, "", nil)

	require.NotNil(t, resolver)
	require.NotNil(t, resolver.cache)
	require.Equal(t, defaultUserGroupRateCacheTTL, resolver.cacheTTL)
	require.NotNil(t, resolver.sf)
	require.Equal(t, "service.gateway", resolver.logComponent)
}

func TestUserGroupRateResolverResolve_FallbackForNilResolverAndInvalidIDs(t *testing.T) {
	var nilResolver *GroupRateResolver
	require.Equal(t, 1.4, nilResolver.Resolve(context.Background(), 101, 202, 1.4))

	resolver := NewGroupRateResolver(nil, nil, time.Second, nil, "service.test", nil)
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 0, 202, 1.4))
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 101, 0, 1.4))
}

func TestUserGroupRateResolverResolve_InvalidCacheEntryLoadsRepoAndCaches(t *testing.T) {
	groupRateMetrics.Hit.Store(0)
	groupRateMetrics.Miss.Store(0)
	groupRateMetrics.Load.Store(0)
	groupRateMetrics.Shared.Store(0)
	groupRateMetrics.Fallback.Store(0)

	rate := 1.7
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	cache := gocache.New(time.Minute, time.Minute)
	cache.Set("101:202", "bad-cache", time.Minute)
	resolver := NewGroupRateResolver(repo, cache, time.Minute, nil, "service.test", nil)

	got := resolver.Resolve(context.Background(), 101, 202, 1.2)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)

	cached, ok := cache.Get("101:202")
	require.True(t, ok)
	require.Equal(t, rate, cached)

	hit, miss, load, _, fallback := GroupRateCacheStats()
	require.Equal(t, int64(0), hit)
	require.Equal(t, int64(1), miss)
	require.Equal(t, int64(1), load)
	require.Equal(t, int64(0), fallback)
}

func (s *userGroupRateRepoHotpathStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	s.calls.Add(1)
	if s.wait != nil {
		<-s.wait
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.rate, nil
}

// resetGatewayRateStatsForTest clears shared group-rate cache counters.
func resetGatewayRateStatsForTest() {
	m := SharedGroupRateMetrics()
	m.Hit.Store(0)
	m.Miss.Store(0)
	m.Load.Store(0)
	m.Shared.Store(0)
	m.Fallback.Store(0)
}

// GroupRateCacheStats returns cumulative group-rate cache counters.
func GroupRateCacheStats() (int64, int64, int64, int64, int64) {
	return groupRateMetrics.Hit.Load(), groupRateMetrics.Miss.Load(), groupRateMetrics.Load.Load(), groupRateMetrics.Shared.Load(), groupRateMetrics.Fallback.Load()
}

func (s *userGroupRateResolverRepoStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.rate, nil
}
