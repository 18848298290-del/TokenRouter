package usage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Dashboard cache freshness determines whether reads query storage
// immediately or return a stored snapshot and request replacement. These
// fixtures capture serialized writes and query ranges, checking invalid-entry
// eviction, cache disablement and aggregation watermark display alongside the
// observed refresh calls.

// usageRepoStub supplies shared dashboard values, captures ranges and
// announces query invocation.
type usageRepoStub struct {
	UsageLogRepository                 // unused usage repository method slots
	stats              *DashboardStats // configured shared base statistics
	rangeStats         *DashboardStats // configured shared range statistics
	err                error           // injected base query failure
	rangeErr           error           // injected range query failure
	calls              int32           // atomic base query invocation count
	rangeCalls         int32           // atomic range query invocation count
	rangeStart         time.Time       // captured inclusive range start
	rangeEnd           time.Time       // captured exclusive range end
	onCall             chan struct{}   // optional query invocation signal channel
}

// dashboardCacheStub captures cache operations and the latest serialized
// write for dashboard tests. Configure callbacks before use; call counters
// are atomic and lastSet is protected by lastSetMu.
type dashboardCacheStub struct {
	// get, set and del provide optional operation outcomes. An absent get
	// reports a cache miss, while absent set and del callbacks succeed after
	// their attempted calls are recorded.
	get      func(ctx context.Context) (string, error)                       // optional serialized read callback
	set      func(ctx context.Context, data string, ttl time.Duration) error // optional serialized write callback
	del      func(ctx context.Context) error                                 // optional serialized eviction callback
	getCalls int32                                                           // atomic attempted read count
	setCalls int32                                                           // atomic attempted write count
	delCalls int32                                                           // atomic attempted eviction count
	// lastSetMu protects lastSet for writer callbacks and readLastEntry.
	// Captured serialization can be inspected independently of the asynchronous
	// callback's atomic attempted-write count.
	lastSetMu sync.Mutex // captured serialization access lock
	lastSet   string     // latest attempted serialized cache value
}

// dashboardAggregationRepoStub supplies a configured watermark or read error
// for dashboard freshness tests.
type dashboardAggregationRepoStub struct {
	watermark time.Time // configured aggregation progress instant
	err       error     // injected watermark read failure
}

// GetDashboardStats counts and optionally announces a dashboard read before
// returning its configured result. The returned statistics pointer is shared;
// configure the fixture before dispatch and observe counters atomically.
func (s *usageRepoStub) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.onCall != nil {
		select {
		case s.onCall <- struct{}{}:
		default:
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.stats, nil
}

// GetDashboardStatsWithRange captures supplied bounds and returns the
// configured range result or base statistics. Captured time fields are read
// after synchronous query completion, while the invocation counter is atomic.
func (s *usageRepoStub) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error) {
	atomic.AddInt32(&s.rangeCalls, 1)
	s.rangeStart = start
	s.rangeEnd = end
	if s.rangeErr != nil {
		return nil, s.rangeErr
	}
	if s.rangeStats != nil {
		return s.rangeStats, nil
	}
	return s.stats, nil
}

// GetDashboardStats counts reads and invokes the optional configured
// callback. An absent callback returns the cache-miss sentinel so dashboard
// tests can exercise the synchronous repository load path.
func (c *dashboardCacheStub) GetDashboardStats(ctx context.Context) (string, error) {
	atomic.AddInt32(&c.getCalls, 1)
	if c.get != nil {
		return c.get(ctx)
	}
	return "", ErrDashboardStatsCacheMiss
}

// SetDashboardStats counts attempted writes and records serialized data under
// lastSetMu before invoking the optional callback. Callback errors are
// returned; the captured value still records the attempted publication.
func (c *dashboardCacheStub) SetDashboardStats(ctx context.Context, data string, ttl time.Duration) error {
	// Capture attempted publication before optional callback failure can return.
	// lastSetMu lets readLastEntry inspect a complete serialized value across refresh dispatch.
	atomic.AddInt32(&c.setCalls, 1)
	c.lastSetMu.Lock()
	c.lastSet = data
	c.lastSetMu.Unlock()
	if c.set != nil {
		return c.set(ctx, data, ttl)
	}
	return nil
}

// DeleteDashboardStats counts attempted eviction and invokes the configured
// callback when present. A missing callback succeeds, allowing
// invalid-payload tests to inspect eviction attempts through their atomic
// counter.
func (c *dashboardCacheStub) DeleteDashboardStats(ctx context.Context) error {
	atomic.AddInt32(&c.delCalls, 1)
	if c.del != nil {
		return c.del(ctx)
	}
	return nil
}

// AggregateRange acknowledges unused rollup writes in the dashboard freshness
// fixture.
func (s *dashboardAggregationRepoStub) AggregateRange(ctx context.Context, start, end time.Time) error {
	return nil
}

// RecomputeRange acknowledges unused repair writes in the dashboard freshness
// fixture.
func (s *dashboardAggregationRepoStub) RecomputeRange(ctx context.Context, start, end time.Time) error {
	return nil
}

// GetAggregationWatermark returns the configured progress or injected error
// for freshness checks. The fixture does not advance that value during
// writes, so each case controls its observed watermark.
func (s *dashboardAggregationRepoStub) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.watermark, nil
}

// UpdateAggregationWatermark acknowledges unused progress writes in the
// dashboard freshness fixture.
func (s *dashboardAggregationRepoStub) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	return nil
}

// CleanupAggregates acknowledges unused rollup cleanup in the dashboard
// freshness fixture.
func (s *dashboardAggregationRepoStub) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	return nil
}

// CleanupUsageLogs acknowledges unused original-row cleanup in the dashboard
// freshness fixture.
func (s *dashboardAggregationRepoStub) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	return nil
}

// CleanupUsageBillingDedup acknowledges unused billing archival in the
// dashboard freshness fixture.
func (s *dashboardAggregationRepoStub) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	return nil
}

// EnsureUsageLogsPartitions acknowledges unused partition preparation in the
// dashboard freshness fixture.
func (s *dashboardAggregationRepoStub) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	return nil
}

// readLastEntry snapshots the latest captured JSON under its mutex and
// decodes it. It marks itself as a test helper and requires decoding success
// before returning the inspected entry.
func (c *dashboardCacheStub) readLastEntry(t *testing.T) dashboardStatsCacheEntry {
	t.Helper()
	c.lastSetMu.Lock()
	data := c.lastSet
	c.lastSetMu.Unlock()

	var entry dashboardStatsCacheEntry
	err := json.Unmarshal([]byte(data), &entry)
	require.NoError(t, err)
	return entry
}

// TestDashboardService_CacheHitFresh supplies a recently inserted cached
// snapshot with epoch aggregation progress. It requires matching returned
// fields, one cache read and no repository read or cache write.
func TestDashboardService_CacheHitFresh(t *testing.T) {
	stats := &DashboardStats{
		TotalUsers:     10,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	entry := dashboardStatsCacheEntry{
		Stats:     stats,
		UpdatedAt: time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return string(payload), nil
		},
	}
	repo := &usageRepoStub{
		stats: &DashboardStats{TotalUsers: 99},
	}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: true},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(0), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.getCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.setCalls))
}

// TestDashboardService_CacheMiss_StoresCache supplies a cache miss and
// repository statistics. It requires one query and write, matching serialized
// statistics and an insertion timestamp near now.
func TestDashboardService_CacheMiss_StoresCache(t *testing.T) {
	stats := &DashboardStats{
		TotalUsers:     7,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "", ErrDashboardStatsCacheMiss
		},
	}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: true},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.getCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.setCalls))
	entry := cache.readLastEntry(t)
	require.Equal(t, stats, entry.Stats)
	require.WithinDuration(t, time.Now(), time.Unix(entry.UpdatedAt, 0), time.Second)
}

// TestDashboardService_CacheDisabled_SkipsCache disables dashboard caching
// while supplying a cache fixture. It requires one repository read and zero
// cache read or write calls.
func TestDashboardService_CacheDisabled_SkipsCache(t *testing.T) {
	stats := &DashboardStats{
		TotalUsers:     3,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "", nil
		},
	}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: false},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.getCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.setCalls))
}

// TestDashboardService_CacheHitStale_TriggersAsyncRefresh supplies an aged
// snapshot and a query notification channel. It requires the aged result
// immediately, an asynchronous read notification and an eventual cache-write
// attempt.
func TestDashboardService_CacheHitStale_TriggersAsyncRefresh(t *testing.T) {
	staleStats := &DashboardStats{
		TotalUsers:     11,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	entry := dashboardStatsCacheEntry{
		Stats:     staleStats,
		UpdatedAt: time.Now().Add(-defaultDashboardStatsFreshTTL * 2).Unix(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return string(payload), nil
		},
	}
	refreshCh := make(chan struct{}, 1)
	repo := &usageRepoStub{
		stats:  &DashboardStats{TotalUsers: 22},
		onCall: refreshCh,
	}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: true},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, staleStats, got)

	// The read notification separates synchronous stale serving from dispatched
	// work. Wait for that query, then inspect an eventual attempted cache publication.
	select {
	case <-refreshCh:
	case <-time.After(1 * time.Second):
		t.Fatal("等待异步刷新超时")
	}
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&cache.setCalls) >= 1
	}, 1*time.Second, 10*time.Millisecond)
}

// TestDashboardService_CacheParseError_EvictsAndRefetches supplies malformed
// cache text and valid repository data. It requires the repository result,
// one eviction and one repository read.
func TestDashboardService_CacheParseError_EvictsAndRefetches(t *testing.T) {
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "not-json", nil
		},
	}
	stats := &DashboardStats{TotalUsers: 9}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: true},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.calls))
}

// TestDashboardService_CacheParseError_RepoFailure supplies malformed
// serialized cache text and injects repository failure. It requires a
// returned error and exactly one captured cache-eviction attempt.
func TestDashboardService_CacheParseError_RepoFailure(t *testing.T) {
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "not-json", nil
		},
	}
	repo := &usageRepoStub{err: errors.New("db down")}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: true},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	_, err := svc.GetDashboardStats(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCalls))
}

// TestDashboardService_StatsUpdatedAtEpochWhenMissing supplies epoch
// watermark data with caching disabled. It requires the exact epoch timestamp
// text and a true staleness flag.
func TestDashboardService_StatsUpdatedAtEpochWhenMissing(t *testing.T) {
	stats := &DashboardStats{}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &Options{Dashboard: DashboardConfig{Enabled: false}}
	svc := NewDashboardService(repo, aggRepo, nil, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1970-01-01T00:00:00Z", got.StatsUpdatedAt)
	require.True(t, got.StatsStale)
}

// TestDashboardService_StatsStaleFalseWhenFresh supplies a current watermark
// and positive interval and lookback values. It requires matching timestamp
// text and a false staleness flag.
func TestDashboardService_StatsStaleFalseWhenFresh(t *testing.T) {
	aggNow := time.Now().UTC().Truncate(time.Second)
	stats := &DashboardStats{}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: aggNow}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: false},
		DashboardAgg: DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LookbackSeconds: 120,
		},
	}
	svc := NewDashboardService(repo, aggRepo, nil, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, aggNow.Format(time.RFC3339), got.StatsUpdatedAt)
	require.False(t, got.StatsStale)
}

// TestDashboardService_AggDisabled_UsesUsageLogsFallback disables aggregation
// with seven-day retention. It requires the range reader, no base dashboard
// call and a UTC-midnight start derived from that captured end.
func TestDashboardService_AggDisabled_UsesUsageLogsFallback(t *testing.T) {
	expected := &DashboardStats{TotalUsers: 42}
	repo := &usageRepoStub{
		rangeStats: expected,
		err:        errors.New("should not call aggregated stats"),
	}
	cfg := &Options{
		Dashboard: DashboardConfig{Enabled: false},
		DashboardAgg: DashboardAggregationConfig{
			Enabled: false,
			Retention: DashboardAggregationRetentionConfig{
				UsageLogsDays: 7,
			},
		},
	}
	svc := NewDashboardService(repo, nil, nil, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(42), got.TotalUsers)
	require.Equal(t, int32(0), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.rangeCalls))
	require.False(t, repo.rangeEnd.IsZero())
	require.Equal(t, truncateToDayUTC(repo.rangeEnd.AddDate(0, 0, -7)), repo.rangeStart)
}
