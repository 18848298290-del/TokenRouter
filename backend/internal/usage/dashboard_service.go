package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// dashboard_service.go serves dashboard statistics and coordinates cached
// snapshot refresh with aggregation freshness reporting.
// Dashboard statistics can be available from a serialized cache even when
// their refresh age has exceeded the freshness interval. GetDashboardStats
// returns that stored snapshot and attempts background replacement, while a
// missing or malformed snapshot causes a synchronous repository read.
//
// Cache age and aggregation lag answer separate questions. Cache age controls
// refreshing; the aggregation watermark, runtime enablement, interval and
// lookback determine StatsStale at each read. Queries have optional range and
// public-counter capabilities discovered during construction. Cache
// publication and eviction receive independent timeout contexts so a query can
// still publish its result after the requesting context ends. Background
// admission is supplied by the application lifecycle owner.

// Dashboard defaults govern snapshot freshness, stored expiration and bounded
// refresh or cache-operation timeouts.
const (
	defaultDashboardStatsFreshTTL       = 15 * time.Second // cached snapshot freshness lifetime
	defaultDashboardStatsCacheTTL       = 30 * time.Second // serialized snapshot expiration lifetime
	defaultDashboardStatsRefreshTimeout = 30 * time.Second // background query and cache timeout
)

// ErrDashboardStatsCacheMiss identifies an absent or unusable cached
// dashboard snapshot for the service's synchronous read path.
var ErrDashboardStatsCacheMiss = errors.New("dashboard cache miss")

// DashboardStatsCache reads and updates serialized administrator dashboard
// statistics for service caching.
type DashboardStatsCache interface {
	// GetDashboardStats returns the stored serialized dashboard entry using ctx
	// for the read. A missing entry reports ErrDashboardStatsCacheMiss so the
	// service can query storage and create another snapshot.
	GetDashboardStats(ctx context.Context) (string, error)
	// SetDashboardStats stores serialized dashboard data for the requested
	// lifetime using ctx. The service passes its configured cache TTL and
	// handles write errors as a best-effort cache publication failure.
	SetDashboardStats(ctx context.Context, data string, ttl time.Duration) error
	// DeleteDashboardStats removes the stored dashboard snapshot using ctx for
	// the operation. The service invokes it after malformed payloads or changed
	// usage settings, and logs deletion errors before subsequent reads proceed.
	DeleteDashboardStats(ctx context.Context) error
}

// dashboardStatsRangeFetcher supplies raw dashboard totals within an explicit
// retained-data query range.
type dashboardStatsRangeFetcher interface {
	// GetDashboardStatsWithRange restricts the Total usage fields to the supplied
	// half-open range. DashboardService uses this capability when aggregation is
	// disabled; today, current-hour and entity counters retain their repository
	// windows.
	GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error)
}

// dashboardPublicStatsFetcher supplies homepage counters through a query
// separate from full administrator statistics.
type dashboardPublicStatsFetcher interface {
	// GetDashboardPublicStats returns homepage token and user counters using the
	// requested data-source choice. DashboardService supplies retention bounds
	// for raw usage totals; the PostgreSQL reader uses its calendar separately
	// for today's tokens.
	GetDashboardPublicStats(ctx context.Context, start, end time.Time, useAggregates bool) (*DashboardPublicStats, error)
}

// dashboardStatsCacheEntry stores dashboard data and the insertion time used
// to assess freshness.
type dashboardStatsCacheEntry struct {
	// Stats contains the decoded snapshot. getCachedDashboardStats evicts an
	// entry whose Stats is nil, then returns the cache-miss sentinel to its
	// caller.
	Stats     *DashboardStats `json:"stats"`      // serialized dashboard data pointer
	UpdatedAt int64           `json:"updated_at"` // cache insertion Unix seconds
}

// DashboardPublicStats carries homepage counters returned by the dedicated
// public reader or dashboard derivation.
type DashboardPublicStats struct {
	TodayTokens int64 // reported today token count
	TotalTokens int64 // reported total token count
	TotalUsers  int64 // reported registered user count
}

// DashboardService supplies administrator reports, cache refreshes and
// aggregation freshness annotations.
type DashboardService struct {
	calendar    timezone.Calendar            // dashboard batch date calendar
	queryCaches *dashboardQueryCaches        // chart and batch query caches
	reporter    func(string, string, ...any) // optional dashboard diagnostic reporter
	readers     DashboardReaders             // optional repository query capabilities
	// runBackground is bound before concurrent service use to the application's
	// task owner. Its return reports admission, though
	// refreshDashboardStatsAsync currently ignores a rejected dispatch.
	runBackground func(string, func()) bool      // background refresh admission function
	usageRepo     UsageLogRepository             // usage report query repository
	aggRepo       DashboardAggregationRepository // optional aggregation watermark reader
	// cache holds the full serialized statistics snapshot. NewDashboardService
	// disables it when Dashboard.Enabled is false; chart and batch caches are
	// constructed separately in queryCaches.
	cache DashboardStatsCache // optional serialized dashboard cache
	// cacheFreshTTL, cacheTTL and refreshTimeout govern refresh age, stored
	// expiration and independent operation deadlines respectively.
	// NewDashboardService resolves positive overrides or their per-field
	// defaults during construction.
	cacheFreshTTL  time.Duration // cached snapshot freshness lifetime
	cacheTTL       time.Duration // serialized snapshot expiration lifetime
	refreshTimeout time.Duration // query and cache operation timeout
	// refreshing admits one asynchronous refresh callback with compare-and-swap.
	// That callback clears the flag when it returns; rejected runner admission
	// leaves the acquired flag set.
	refreshing int32 // asynchronous refresh admission flag
	// aggEnabled, aggInterval, aggLookback and aggUsageDays hold
	// construction-time deployment rules. Runtime settings can disable
	// aggregation or replace its interval; missing aggRepo disables aggregate
	// eligibility during construction.
	aggEnabled   bool          // deployment aggregation availability flag
	aggInterval  time.Duration // deployment aggregation schedule interval
	aggLookback  time.Duration // aggregation late record lookback
	aggUsageDays int           // raw dashboard retention day count
	// preAggregation is bound during construction before concurrent reads. Its
	// listeners can dispatch shared snapshot eviction when usage aggregation
	// settings change.
	preAggregation AggregationSettings // optional runtime aggregation settings
}

// DashboardReaders holds optional range and public-counter capabilities
// selected during service construction.
type DashboardReaders struct {
	// Range and Public are discovered from usageRepo unless NewDashboardService
	// receives a reader set. The first supplied set replaces both discovered
	// capabilities, including nil members.
	Range  dashboardStatsRangeFetcher  // optional raw range capability
	Public dashboardPublicStatsFetcher // optional public counter capability
}

// NewDashboardService constructs an idle dashboard with repository
// capabilities, caches and query timing. The first supplied reader set
// replaces discovery completely; disabled dashboard caching clears cache, and
// missing aggRepo disables aggregate eligibility.
func NewDashboardService(usageRepo UsageLogRepository, aggRepo DashboardAggregationRepository, cache DashboardStatsCache, cfg *Options, readers ...DashboardReaders) *DashboardService {
	calendar := timezone.NewCalendar(time.Local)
	// Direct construction dispatches callbacks in goroutines. A supplied
	// RunBackground can reject admission, so asynchronous refresh relies on its
	// accepted callback running to release the refreshing flag.
	background := func(_ string, fn func()) bool { go fn(); return true }
	if cfg != nil && cfg.RunBackground != nil {
		background = cfg.RunBackground
	}
	sources := DashboardReaders{}
	sources.Range, _ = usageRepo.(dashboardStatsRangeFetcher)
	sources.Public, _ = usageRepo.(dashboardPublicStatsFetcher)
	if len(readers) > 0 {
		sources = readers[0]
	}
	freshTTL := defaultDashboardStatsFreshTTL
	cacheTTL := defaultDashboardStatsCacheTTL
	refreshTimeout := defaultDashboardStatsRefreshTimeout
	aggEnabled := true
	aggInterval := time.Minute
	aggLookback := 2 * time.Minute
	aggUsageDays := 90
	if cfg != nil {
		calendar = cfg.Calendar
		if !cfg.Dashboard.Enabled {
			cache = nil
		}
		if cfg.Dashboard.StatsFreshTTLSeconds > 0 {
			freshTTL = time.Duration(cfg.Dashboard.StatsFreshTTLSeconds) * time.Second
		}
		if cfg.Dashboard.StatsTTLSeconds > 0 {
			cacheTTL = time.Duration(cfg.Dashboard.StatsTTLSeconds) * time.Second
		}
		if cfg.Dashboard.StatsRefreshTimeoutSeconds > 0 {
			refreshTimeout = time.Duration(cfg.Dashboard.StatsRefreshTimeoutSeconds) * time.Second
		}
		aggEnabled = cfg.DashboardAgg.Enabled
		if cfg.DashboardAgg.IntervalSeconds > 0 {
			aggInterval = time.Duration(cfg.DashboardAgg.IntervalSeconds) * time.Second
		}
		if cfg.DashboardAgg.LookbackSeconds > 0 {
			aggLookback = time.Duration(cfg.DashboardAgg.LookbackSeconds) * time.Second
		}
		if cfg.DashboardAgg.Retention.UsageLogsDays > 0 {
			aggUsageDays = cfg.DashboardAgg.Retention.UsageLogsDays
		}
	}
	if aggRepo == nil {
		aggEnabled = false
	}
	return &DashboardService{
		queryCaches: newDashboardQueryCaches(), reporter: usageReporter(cfg),
		calendar:       calendar,
		runBackground:  background,
		readers:        sources,
		usageRepo:      usageRepo,
		aggRepo:        aggRepo,
		cache:          cache,
		cacheFreshTTL:  freshTTL,
		cacheTTL:       cacheTTL,
		refreshTimeout: refreshTimeout,
		aggEnabled:     aggEnabled,
		aggInterval:    aggInterval,
		aggLookback:    aggLookback,
		aggUsageDays:   aggUsageDays,
	}
}

// SetPreAggregationSettings binds runtime aggregation settings before
// concurrent dashboard reads. When usage settings change, its listener asks
// the background runner to evict the shared serialized snapshot; repeated
// bindings can register additional listeners. Nil services return immediately.
func (s *DashboardService) SetPreAggregationSettings(settings AggregationSettings) {
	if s == nil {
		return
	}
	s.preAggregation = settings
	if settings != nil {
		settings.RegisterListener(func(previous, next PreAggregationSettings) {
			if previous.Usage != next.Usage {
				s.runBackground("service/dashboard_service.go:SetPreAggregationSettings", func() { s.evictDashboardStatsCache(nil) })
			}
		})
	}
}

// GetDashboardStats returns dashboard statistics with aggregation lag
// evaluated for the current read. A valid cache entry supplies the result
// even after its freshness interval, while unusable or missing cached data
// triggers a synchronous repository refresh.
//
// Cache freshness uses the entry's insertion time, independently of the
// aggregation watermark exposed in StatsUpdatedAt. Stale cache data schedules
// background replacement and remains available immediately. Cache read,
// eviction and publication failures are logged where applicable; a failed
// synchronous statistics query returns an error to the caller.
func (s *DashboardService) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	if s.cache != nil {
		cached, fresh, err := s.getCachedDashboardStats(ctx)
		if err == nil && cached != nil {
			s.refreshAggregationStaleness(cached)
			// Cache age controls replacement while aggregation lag is annotated separately.
			// Return this available snapshot now and dispatch refresh work for later callers.
			if !fresh {
				s.refreshDashboardStatsAsync()
			}
			return cached, nil
		}
		if err != nil && !errors.Is(err, ErrDashboardStatsCacheMiss) {
			s.logMessage("service.dashboard", "[INFO] [Dashboard] Failed to read dashboard cache: %v", err)
		}
	}

	// A cache miss or failed read leaves no usable snapshot for this response.
	// Query synchronously so a valid result can replace the entry and serve the
	// caller.
	stats, err := s.refreshDashboardStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("get dashboard stats: %w", err)
	}
	return stats, nil
}

// GetPublicDashboardStats returns homepage counters through the optional
// public reader, supplying a UTC retention range and current aggregation
// choice. If that capability is absent, it derives the counters from
// GetDashboardStats.
func (s *DashboardService) GetPublicDashboardStats(ctx context.Context) (*DashboardPublicStats, error) {
	if fetcher := s.readers.Public; fetcher != nil {
		now := time.Now().UTC()
		start := truncateToDayUTC(now.AddDate(0, 0, -s.aggUsageDays))
		stats, err := fetcher.GetDashboardPublicStats(ctx, start, now, s.aggregationEnabled(ctx))
		if err != nil {
			return nil, fmt.Errorf("get public dashboard stats: %w", err)
		}
		return stats, nil
	}

	// A dedicated public reader is optional. Derive the homepage counters from the
	// complete dashboard query when that capability was not bound during construction.
	stats, err := s.GetDashboardStats(ctx)
	if err != nil {
		return nil, err
	}
	return &DashboardPublicStats{
		TodayTokens: stats.TodayTokens,
		TotalTokens: stats.TotalTokens,
		TotalUsers:  stats.TotalUsers,
	}, nil
}

// GetUsageTrendWithFilters queries the repository for the selected usage
// trend and wraps failures.
func (s *DashboardService) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]TrendDataPoint, error) {
	trend, err := s.usageRepo.GetUsageTrendWithFilters(ctx, startTime, endTime, granularity, userID, apiKeyID, providerID, groupID, model, requestType, stream, billingType)
	if err != nil {
		return nil, fmt.Errorf("get usage trend with filters: %w", err)
	}
	return trend, nil
}

// GetUsageTrendWithUsageFilters uses the unified-filter capability when the
// repository supplies it. Otherwise it calls the reduced signature, which
// omits team, ownership, personal, request-ID, billing-mode, model-source and
// compaction selectors.
func (s *DashboardService) GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) ([]TrendDataPoint, error) {
	// filteredRepository supplies the unified selectors for this trend query.
	type filteredRepository interface {
		// GetUsageTrendWithUsageFilters receives the complete selector set and
		// supplied context for trend loading. Implementations apply team,
		// ownership, request identity and compaction conditions before returning
		// the selected buckets.
		GetUsageTrendWithUsageFilters(context.Context, time.Time, time.Time, string, UsageLogFilters) ([]TrendDataPoint, error)
	}
	if repo, ok := s.usageRepo.(filteredRepository); ok {
		trend, err := repo.GetUsageTrendWithUsageFilters(ctx, startTime, endTime, granularity, filters)
		if err != nil {
			return nil, fmt.Errorf("get usage trend with usage filters: %w", err)
		}
		return trend, nil
	}
	// The reduced repository signature cannot carry the complete selectors. Delegate
	// its supported fields so older readers remain callable. Full-scope queries
	// require the optional capability.
	return s.GetUsageTrendWithFilters(ctx, startTime, endTime, granularity, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.Model, filters.RequestType, filters.Stream, filters.BillingType)
}

// GetModelStatsWithFilters queries the repository for the selected model
// grouping and wraps failures.
func (s *DashboardService) GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) ([]ModelStat, error) {
	stats, err := s.usageRepo.GetModelStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, requestType, stream, billingType)
	if err != nil {
		return nil, fmt.Errorf("get model stats with filters: %w", err)
	}
	return stats, nil
}

// GetModelStatsWithFiltersBySource normalizes the model dimension and uses
// source-aware storage for nonrequested dimensions. A repository lacking that
// capability answers through its requested-model grouping, so callers cannot
// assume source-specific output there.
func (s *DashboardService) GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8, modelSource string) ([]ModelStat, error) {
	normalizedSource := NormalizeModelSource(modelSource)
	if normalizedSource == ModelSourceRequested {
		return s.GetModelStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, requestType, stream, billingType)
	}

	// modelStatsBySourceRepo supplies model grouping for a selected reporting
	// dimension.
	type modelStatsBySourceRepo interface {
		// GetModelStatsWithFiltersBySource groups model statistics by the selected
		// normalized source using the supplied context. Repositories supporting
		// this capability can distinguish requested, upstream and mapping output
		// under the reduced selectors.
		GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) ([]ModelStat, error)
	}

	if sourceRepo, ok := s.usageRepo.(modelStatsBySourceRepo); ok {
		stats, err := sourceRepo.GetModelStatsWithFiltersBySource(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, requestType, stream, billingType, normalizedSource)
		if err != nil {
			return nil, fmt.Errorf("get model stats with filters by source: %w", err)
		}
		return stats, nil
	}

	// A reader lacking source-aware grouping can still answer its requested-model
	// query. This result follows that grouping even when the caller selected another dimension.
	return s.GetModelStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, requestType, stream, billingType)
}

// GetModelStatsWithUsageFiltersBySource uses full selectors and normalized
// grouping when supported. Its reduced-signature path omits team, ownership,
// personal, model, request-ID, billing-mode and compaction selectors while
// retaining source-aware dispatch where available.
func (s *DashboardService) GetModelStatsWithUsageFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, modelSource string) ([]ModelStat, error) {
	// filteredRepository supplies complete selectors and source-aware model
	// grouping for dashboard reads.
	type filteredRepository interface {
		// GetModelStatsWithUsageFiltersBySource applies the complete selector set
		// and normalized model dimension to its query. The supplied context
		// controls loading, and the resulting grouping retains repository-specific
		// aggregate eligibility.
		GetModelStatsWithUsageFiltersBySource(context.Context, time.Time, time.Time, UsageLogFilters, string) ([]ModelStat, error)
	}
	if repo, ok := s.usageRepo.(filteredRepository); ok {
		stats, err := repo.GetModelStatsWithUsageFiltersBySource(ctx, startTime, endTime, filters, NormalizeModelSource(modelSource))
		if err != nil {
			return nil, fmt.Errorf("get model stats with usage filters: %w", err)
		}
		return stats, nil
	}
	// Older repository signatures omit several unified selectors. Pass the supported
	// fields and selected model source while full-scope callers require the optional capability.
	return s.GetModelStatsWithFiltersBySource(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType, modelSource)
}

// GetGroupStatsWithFilters queries the repository for the selected group
// grouping and wraps failures.
func (s *DashboardService) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) ([]GroupStat, error) {
	stats, err := s.usageRepo.GetGroupStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, requestType, stream, billingType)
	if err != nil {
		return nil, fmt.Errorf("get group stats with filters: %w", err)
	}
	return stats, nil
}

// GetGroupStatsWithUsageFilters uses full selectors when supported by the
// repository. Its reduced-signature path omits team, ownership, personal,
// model, request-ID, billing-mode and compaction selectors, so those scopes
// require the optional capability.
func (s *DashboardService) GetGroupStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) ([]GroupStat, error) {
	// filteredRepository supplies complete selectors for a dashboard
	// group-statistics query.
	type filteredRepository interface {
		// GetGroupStatsWithUsageFilters applies the complete selector set with the
		// supplied context before grouping usage by group. Implementations retain
		// team, ownership, request identity and compaction conditions required by
		// these reads.
		GetGroupStatsWithUsageFilters(context.Context, time.Time, time.Time, UsageLogFilters) ([]GroupStat, error)
	}
	if repo, ok := s.usageRepo.(filteredRepository); ok {
		stats, err := repo.GetGroupStatsWithUsageFilters(ctx, startTime, endTime, filters)
		if err != nil {
			return nil, fmt.Errorf("get group stats with usage filters: %w", err)
		}
		return stats, nil
	}
	// Older group readers accept a reduced selector set. Delegate the supported fields
	// here while queries requiring team or other omitted scope use the optional capability.
	return s.GetGroupStatsWithFilters(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType)
}

// GetGroupUsageSummary returns group charges for today, yesterday and
// retained total history. The caller supplies the calendar's today midnight;
// the repository applies its own aggregate eligibility and raw-query
// handling.
func (s *DashboardService) GetGroupUsageSummary(ctx context.Context, todayStart time.Time) ([]GroupUsageSummary, error) {
	results, err := s.usageRepo.GetAllGroupUsageSummary(ctx, todayStart)
	if err != nil {
		return nil, fmt.Errorf("get group usage summary: %w", err)
	}
	return results, nil
}

// getCachedDashboardStats decodes the stored entry and reports freshness from
// its insertion time. Invalid JSON or nil statistics trigger eviction and a
// cache miss; cache-read errors are returned for the caller's refresh path.
func (s *DashboardService) getCachedDashboardStats(ctx context.Context) (*DashboardStats, bool, error) {
	data, err := s.cache.GetDashboardStats(ctx)
	if err != nil {
		return nil, false, err
	}

	var entry dashboardStatsCacheEntry
	// Malformed serialized data cannot supply a dashboard snapshot. Evict it and
	// report a miss so GetDashboardStats can replace it through a repository query.
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		s.evictDashboardStatsCache(err)
		return nil, false, ErrDashboardStatsCacheMiss
	}
	// A valid JSON envelope can still lack statistics. Remove that unusable entry
	// before the caller rebuilds its data through the same cache-miss query path.
	if entry.Stats == nil {
		s.evictDashboardStatsCache(errors.New("dashboard cache is missing statistics"))
		return nil, false, ErrDashboardStatsCacheMiss
	}

	age := time.Since(time.Unix(entry.UpdatedAt, 0))
	return entry.Stats, age <= s.cacheFreshTTL, nil
}

// refreshDashboardStats queries statistics, annotates aggregation status and
// attempts serialized cache publication. The caller context controls the
// query, while cache publication uses its own timeout and leaves the fetched
// result available on cache failure.
func (s *DashboardService) refreshDashboardStats(ctx context.Context) (*DashboardStats, error) {
	stats, err := s.fetchDashboardStats(ctx)
	if err != nil {
		return nil, err
	}
	s.applyAggregationStatus(ctx, stats)
	cacheCtx, cancel := s.cacheOperationContext()
	defer cancel()
	s.saveDashboardStatsCache(cacheCtx, stats)
	return stats, nil
}

// refreshDashboardStatsAsync acquires the local refresh flag and dispatches
// one timeout-bounded refresh when a cache exists. An active callback keeps
// other stale reads from dispatching another refresh while it queries and
// publishes replacement data.
//
// The callback clears refreshing on exit and uses background contexts for
// querying and cache publication. Runner admission happens after the flag is
// acquired, and its boolean result is ignored here. If the lifecycle runner
// rejects dispatch, the callback cannot release that flag, so later
// asynchronous refresh attempts remain suppressed.
func (s *DashboardService) refreshDashboardStatsAsync() {
	if s.cache == nil {
		return
	}
	// Stale readers can arrive together. Admit one replacement callback so concurrent
	// requests can reuse the stored snapshot while that callback refreshes its statistics.
	if !atomic.CompareAndSwapInt32(&s.refreshing, 0, 1) {
		return
	}
	// The callback owns flag release after admission. This call ignores rejected
	// dispatch, which can leave refreshing set when the application's task owner stops.
	s.runBackground("service/dashboard_service.go:refreshDashboardStatsAsync", func() {
		defer atomic.StoreInt32(&s.refreshing, 0)

		ctx, cancel := context.WithTimeout(context.Background(), s.refreshTimeout)
		defer cancel()

		stats, err := s.fetchDashboardStats(ctx)
		// A failed replacement query leaves the existing serialized snapshot
		// available until its expiration. Log the refresh failure and end this
		// admitted callback before attempting cache publication.
		if err != nil {
			s.logMessage("service.dashboard", "[INFO] [Dashboard] Failed to refresh dashboard cache asynchronously: %v", err)
			return
		}
		s.applyAggregationStatus(ctx, stats)
		cacheCtx, cancel := s.cacheOperationContext()
		defer cancel()
		s.saveDashboardStatsCache(cacheCtx, stats)
	})
}

// fetchDashboardStats selects the raw range capability when aggregation is
// disabled, using the configured UTC retention start. Other cases call the
// base dashboard query, whose own repository policy determines its data
// source.
func (s *DashboardService) fetchDashboardStats(ctx context.Context) (*DashboardStats, error) {
	if !s.aggregationEnabled(ctx) {
		if fetcher := s.readers.Range; fetcher != nil {
			now := time.Now().UTC()
			start := truncateToDayUTC(now.AddDate(0, 0, -s.aggUsageDays))
			return fetcher.GetDashboardStatsWithRange(ctx, start, now)
		}
	}
	// A missing range capability still permits the base dashboard query to
	// choose its own storage policy. This path keeps older repositories usable
	// when a reduced raw reader was not bound.
	return s.usageRepo.GetDashboardStats(ctx)
}

// saveDashboardStatsCache serializes statistics with the current insertion
// time and attempts storage for cacheTTL. Missing caches or statistics return
// immediately; encoding and write failures are logged while the caller
// retains its result.
func (s *DashboardService) saveDashboardStatsCache(ctx context.Context, stats *DashboardStats) {
	if s.cache == nil || stats == nil {
		return
	}

	entry := dashboardStatsCacheEntry{
		Stats:     stats,
		UpdatedAt: time.Now().Unix(),
	}
	data, err := json.Marshal(entry)
	// Serialization failure leaves the fetched statistics available to the
	// current caller. Log the cache-publication error and keep later reads
	// eligible to query or use their remaining stored entry.
	if err != nil {
		s.logMessage("service.dashboard", "[INFO] [Dashboard] Failed to serialize dashboard cache: %v", err)
		return
	}

	// A failed cache write does not change the statistics already fetched for
	// this response. Record the failure while later readers retain their
	// ordinary lookup and refresh paths.
	if err := s.cache.SetDashboardStats(ctx, string(data), s.cacheTTL); err != nil {
		s.logMessage("service.dashboard", "[INFO] [Dashboard] Failed to write dashboard cache: %v", err)
	}
}

// evictDashboardStatsCache attempts shared snapshot removal with an
// independent timeout context. A nil cache returns immediately; deletion
// failures and a supplied reason are logged for later synchronous or
// background refresh diagnosis.
func (s *DashboardService) evictDashboardStatsCache(reason error) {
	if s.cache == nil {
		return
	}
	cacheCtx, cancel := s.cacheOperationContext()
	defer cancel()

	// Eviction is best effort after unusable data or changed settings. Log its
	// failure so a remaining stored entry can be diagnosed while later reads
	// continue their normal refresh handling.
	if err := s.cache.DeleteDashboardStats(cacheCtx); err != nil {
		s.logMessage("service.dashboard", "[INFO] [Dashboard] Failed to clear dashboard cache: %v", err)
	}
	if reason != nil {
		s.logMessage("service.dashboard", "[Dashboard] Invalid dashboard cache cleared: %v", reason)
	}
}

// cacheOperationContext returns an independent background context bounded by
// refreshTimeout for cache operations. The caller invokes its cancel function
// after use, even when the cache call returns before that timeout.
func (s *DashboardService) cacheOperationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.refreshTimeout)
}

// applyAggregationStatus updates statistics with the reported aggregation
// watermark and current staleness decision. Nil statistics return
// immediately; the UTC timestamp and freshness fields are assigned together
// before cache publication.
func (s *DashboardService) applyAggregationStatus(ctx context.Context, stats *DashboardStats) {
	if stats == nil {
		return
	}
	updatedAt := s.fetchAggregationUpdatedAt(ctx)
	stats.StatsUpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	stats.StatsStale = s.isAggregationStale(updatedAt, time.Now().UTC())
}

// refreshAggregationStaleness reevaluates the cached snapshot's lag using its
// stored watermark text. Nil statistics return immediately; invalid or empty
// watermark text becomes the epoch for the staleness calculation.
func (s *DashboardService) refreshAggregationStaleness(stats *DashboardStats) {
	if stats == nil {
		return
	}
	updatedAt := parseStatsUpdatedAt(stats.StatsUpdatedAt)
	stats.StatsStale = s.isAggregationStale(updatedAt, time.Now().UTC())
}

// fetchAggregationUpdatedAt returns the watermark in UTC or the epoch when it
// is unavailable. Missing repositories, read errors and zero times select
// that sentinel, which is treated as stale by the dashboard.
func (s *DashboardService) fetchAggregationUpdatedAt(ctx context.Context) time.Time {
	if s.aggRepo == nil {
		return time.Unix(0, 0).UTC()
	}
	updatedAt, err := s.aggRepo.GetAggregationWatermark(ctx)
	// Failed watermark reads leave progress unknown. Return the same epoch marker
	// used for absent progress so the caller reports stale statistics for this snapshot.
	if err != nil {
		s.logMessage("service.dashboard", "[INFO] [Dashboard] Failed to read aggregation watermark: %v", err)
		return time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return updatedAt.UTC()
}

// isAggregationStale reports whether enabled aggregation has exceeded its
// expected lag allowance. Disabled aggregation or a watermark at or before
// the epoch is stale immediately, so unknown progress cannot appear current
// from its cached timestamp.
//
// For observed progress, the threshold combines the aggregation interval with
// its configured lookback. Runtime settings replace the interval used for
// this check. The result compares elapsed watermark age with that combined
// threshold, independently of when the serialized dashboard snapshot was
// inserted.
func (s *DashboardService) isAggregationStale(updatedAt, now time.Time) bool {
	if !s.aggregationEnabled(context.Background()) {
		return true
	}
	epoch := time.Unix(0, 0).UTC()
	if !updatedAt.After(epoch) {
		return true
	}
	interval := s.aggInterval
	if s.preAggregation != nil {
		interval = time.Duration(s.preAggregation.Resolve(context.Background()).Usage.IntervalSeconds) * time.Second
	}
	// Include the late-record lookback in the expected aggregation delay.
	threshold := interval + s.aggLookback
	return now.Sub(updatedAt) > threshold
}

// aggregationEnabled reports aggregate eligibility after deployment and
// runtime settings are considered. Nil services or a disabled deployment
// return false; an eligible service without runtime settings keeps deployment
// aggregation enabled.
func (s *DashboardService) aggregationEnabled(ctx context.Context) bool {
	if s == nil || !s.aggEnabled {
		return false
	}
	if s.preAggregation == nil {
		return true
	}
	return s.preAggregation.UsageEnabled(ctx)
}

// parseStatsUpdatedAt returns an RFC3339 watermark in UTC or the epoch
// sentinel. Empty or invalid text selects the sentinel so cached staleness
// checks can use one unavailable-progress representation.
func parseStatsUpdatedAt(raw string) time.Time {
	if raw == "" {
		return time.Unix(0, 0).UTC()
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	// Unparseable stored text supplies no usable progress instant. Use the epoch
	// marker so cached staleness follows the same rule as an absent aggregation watermark.
	if err != nil {
		return time.Unix(0, 0).UTC()
	}
	return parsed.UTC()
}

// GetAPIKeyUsageTrend queries the repository for ranked key trends and wraps
// failures.
func (s *DashboardService) GetAPIKeyUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]APIKeyUsageTrendPoint, error) {
	trend, err := s.usageRepo.GetAPIKeyUsageTrend(ctx, startTime, endTime, granularity, limit)
	if err != nil {
		return nil, fmt.Errorf("get api key usage trend: %w", err)
	}
	return trend, nil
}

// GetUserUsageTrend queries the repository for ranked user trends and wraps
// failures.
func (s *DashboardService) GetUserUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]UserUsageTrendPoint, error) {
	trend, err := s.usageRepo.GetUserUsageTrend(ctx, startTime, endTime, granularity, limit)
	if err != nil {
		return nil, fmt.Errorf("get user usage trend: %w", err)
	}
	return trend, nil
}

// GetUserSpendingRanking queries the repository for spending rows and range
// totals, wrapping failures.
func (s *DashboardService) GetUserSpendingRanking(ctx context.Context, startTime, endTime time.Time, limit int) (*UserSpendingRankingResponse, error) {
	ranking, err := s.usageRepo.GetUserSpendingRanking(ctx, startTime, endTime, limit)
	if err != nil {
		return nil, fmt.Errorf("get user spending ranking: %w", err)
	}
	return ranking, nil
}

// GetUserBreakdownStats queries the repository for users within the selected
// dimensions and wraps failures.
func (s *DashboardService) GetUserBreakdownStats(ctx context.Context, startTime, endTime time.Time, dim UserBreakdownDimension, limit int) ([]UserBreakdownItem, error) {
	stats, err := s.usageRepo.GetUserBreakdownStats(ctx, startTime, endTime, dim, limit)
	if err != nil {
		return nil, fmt.Errorf("get user breakdown stats: %w", err)
	}
	return stats, nil
}

// GetBatchUserUsageStats queries batch user charges using the repository's
// range defaults. PostgreSQL zero bounds select approximately thirty days
// through now, while today's charges use the repository calendar separately.
func (s *DashboardService) GetBatchUserUsageStats(ctx context.Context, userIDs []int64, startTime, endTime time.Time) (map[int64]*BatchUserUsageStats, error) {
	stats, err := s.usageRepo.GetBatchUserUsageStats(ctx, userIDs, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get batch user usage stats: %w", err)
	}
	return stats, nil
}

// GetBatchAPIKeyUsageStats queries batch key charges using the repository's
// range defaults. PostgreSQL zero bounds select approximately thirty days
// through now, while today's charges use the repository calendar separately.
func (s *DashboardService) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	stats, err := s.usageRepo.GetBatchAPIKeyUsageStats(ctx, apiKeyIDs, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get batch api key usage stats: %w", err)
	}
	return stats, nil
}

// SetBackgroundRunner replaces the refresh-task dispatcher when fn is nonnil.
// Bind it before concurrent dashboard use, because service reads and
// listeners access the stored callback without synchronization around
// replacement.
func (s *DashboardService) SetBackgroundRunner(fn func(string, func()) bool) {
	if fn != nil {
		s.runBackground = fn
	}
}

// logMessage reports dashboard diagnostics through the configured reporter
// when present. Nil services or absent reporters use logUsage, keeping cache
// and query failure messages available for directly constructed service
// instances.
func (s *DashboardService) logMessage(component, format string, args ...any) {
	if s != nil && s.reporter != nil {
		s.reporter(component, format, args...)
		return
	}
	// Services constructed without the injected reporter still publish cache and
	// query diagnostics through logUsage, preserving visibility of best-effort cache failures.
	logUsage(component, format, args...)
}
