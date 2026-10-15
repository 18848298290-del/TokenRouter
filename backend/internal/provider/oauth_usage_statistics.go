package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// oauth_usage_statistics.go reads local provider usage totals and attaches
// statistics to quota displays.
// Local provider totals supplement upstream quota windows even when statistics
// reads partially fail. A combined two-window query is preferred when
// available, then separate reads preserve successful observations; daily
// batches similarly prefer one query before bounded best-effort per-row reads.
//
// Successful nil statistics become zero-valued copies in pair and batch paths.
// Failed fallback rows also receive zeros in daily batches, while pair reads
// retain nil to show missing observations. Window attachment has its own
// one-minute row-keyed cache and only writes FiveHour even when weekly windows
// trigger the query. That cache does not compare a new session start, so a
// recent row entry can retain prior-window totals until it ages. Readers and
// callback clocks remain borrowed and stable through each operation.

// LocalUsageStats reads local request, token, and cost totals for provider
// windows.
type LocalUsageStats interface {
	// GetProviderWindowStats reads totals since the supplied start. Pair and
	// batch consumers normalize nil success, while AddWindowStats requires a
	// nonnil response before copying the scalar fields into its cache.
	GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error)
	// GetProviderTodayStats reads the repository-defined current-day totals.
	// Return a nonnil successful response because GetTodayStats copies its
	// scalar request, token, and cost fields before returning caller-owned data.
	GetProviderTodayStats(context.Context, int64) (*WindowStats, error)
}

// LocalUsageStatsBatch optionally reads multiple provider totals in one shared
// date window.
type LocalUsageStatsBatch interface {
	// GetProviderWindowStatsBatch reads a common start across supplied provider
	// IDs. Missing or nil successful rows mean zero totals to display callers;
	// a whole-query error selects bounded per-provider fallback.
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*WindowStats, error)
}

// LocalUsageStatsPair optionally reads two provider windows through one
// combined query.
type LocalUsageStatsPair interface {
	// GetProviderWindowStatsPair reads two starts for one provider. Successful
	// nil members mean observed zero totals; a combined error permits ordered
	// single-read fallback while its caller context remains available.
	GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*WindowStats, *WindowStats, error)
}

// LocalUsageStatisticsOptions supplies observation time, daily window origin,
// and failure diagnostics.
type LocalUsageStatisticsOptions struct {
	// Now is required by window-cache attachment and supplies observation time.
	// Construction retains it unchanged, so callers choose a clock consistent
	// with their session data.
	Now func() time.Time
	// Today is required by daily batch reads and chooses their common start.
	// Its timezone and calendar are supplied by assembly rather than normalized
	// here.
	Today func() time.Time
	// Log is required when attachment statistics fail. That branch records the
	// error and retains the existing view instead of attaching unobserved totals.
	Log func(string, ...any)
}

// LocalUsageStatistics reads local totals and caches short-lived provider
// window observations.
type LocalUsageStatistics struct {
	// usageLogRepo and cache retain shared service references; options copies
	// callback values. Configure those dependencies before calls and keep them
	// stable while statistics reads or cache publication are active.
	usageLogRepo LocalUsageStats             // shared local statistics reader
	cache        *OAuthUsageCache            // shared short-lived totals cache
	options      LocalUsageStatisticsOptions // retained clock and diagnostics
}

// NewLocalUsageStatistics retains the reader, cache, and supplied callbacks
// without defaults. Configure reached Now, Today, and Log callbacks and retain
// shared dependencies through their consumer calls; pair lookup only requires
// the reader.
func NewLocalUsageStatistics(reader LocalUsageStats, cache *OAuthUsageCache, options LocalUsageStatisticsOptions) *LocalUsageStatistics {
	return &LocalUsageStatistics{reader, cache, options}
}

// GetWindowPair returns independently copied observations for two starts on
// one provider. A supported combined query wins on success, and its nil values
// normalize to zero totals.
//
// Combined failure falls back to ordered single reads unless ctx is canceled.
// Each single failure yields nil while preserving an earlier successful
// result; cancellation after the first read leaves the second absent. These
// errors are suppressed so display consumers can retain partial statistics.
// Provide a nonnil reader and context, and keep returned repository values
// stable while this operation copies them.
func (s *LocalUsageStatistics) GetWindowPair(ctx context.Context, id int64, first, second time.Time) (*WindowStats, *WindowStats) {
	if reader, ok := s.usageLogRepo.(LocalUsageStatsPair); ok {
		a, b, err := reader.GetProviderWindowStatsPair(ctx, id, first, second)
		if err == nil {
			return normalizedLocalWindowStats(a), normalizedLocalWindowStats(b)
		}
		if ctx.Err() != nil {
			return nil, nil
		}
	}
	// Absent or failed combined support selects separate observations so a
	// successful first window can remain visible when the other read fails or
	// cancellation is observed before its start.
	a, err := s.usageLogRepo.GetProviderWindowStats(ctx, id, first)
	if err != nil {
		a = nil
	} else {
		a = normalizedLocalWindowStats(a)
	}
	if ctx.Err() != nil {
		return a, nil
	}
	b, err := s.usageLogRepo.GetProviderWindowStats(ctx, id, second)
	if err != nil {
		b = nil
	} else {
		b = normalizedLocalWindowStats(b)
	}
	return a, b
}

// normalizedLocalWindowStats copies scalar totals or replaces nil success with
// zeros. Keep the source stable during copying; callers receive a new pointer
// suitable for independent display attachment.
func normalizedLocalWindowStats(value *WindowStats) *WindowStats {
	if value == nil {
		return &WindowStats{}
	}
	return clonePointer(value)
}

// AddWindowStats attaches local session totals to FiveHour on a stable nonnil
// usage view and record. Supply a usable reader and cache plus reached clock
// and diagnostic callbacks. Any supported window can trigger the query, but
// only FiveHour receives the result.
//
// A recent row-keyed cache entry wins even if the session start has changed.
// Cache misses derive that start through RuntimeConfig and require a nonnil
// successful statistics response; query failure is logged and leaves the view
// unchanged. Publication copies scalar totals into the cache before
// attachment, so callers can edit the resulting view independently of later
// cache reads.
func (s *LocalUsageStatistics) AddWindowStats(ctx context.Context, provider *Record, usage *UsageInfo) {
	// A weekly window also triggers statistics loading, although attachment
	// below requires FiveHour. This keeps the existing row-cache side effect
	// separate from which display window receives totals.
	if usage.FiveHour == nil && usage.SevenDay == nil && usage.SevenDaySonnet == nil && usage.SevenDayFable == nil {
		return
	}

	var windowStats *WindowStats
	if cache, ok := s.cache.LoadWindow(provider.ID); ok && s.options.Now().Sub(cache.Timestamp) < OAuthUsageWindowStatsCacheTTL {
		windowStats = cache.Stats
	}

	if windowStats == nil {
		// Derive a session start only after the row cache misses. A still-fresh
		// cached entry can retain previous-window totals until its age check expires.
		startTime := (&RuntimeConfig{Extra: provider.Extra, Concurrency: provider.Concurrency, SessionWindowStart: provider.SessionWindowStart, SessionWindowEnd: provider.SessionWindowEnd}).GetCurrentWindowStartTime(s.options.Now())

		stats, err := s.usageLogRepo.GetProviderWindowStats(ctx, provider.ID, startTime)
		if err != nil {
			s.options.Log("Failed to get window stats for provider %d: %v", provider.ID, err)
			return
		}

		windowStats = &WindowStats{
			Requests:     stats.Requests,
			Tokens:       stats.Tokens,
			Cost:         stats.Cost,
			StandardCost: stats.StandardCost,
			UserCost:     stats.UserCost,
		}

		s.cache.StoreWindow(provider.ID, &OAuthWindowStatsCache{
			Stats:     windowStats,
			Timestamp: s.options.Now(),
		})
	}

	if usage.FiveHour != nil {
		usage.FiveHour.WindowStats = windowStats
	}
}

// GetTodayStats returns copied scalar totals from the repository current-day
// query. A nonnil successful response is required; errors are wrapped for
// display callers, which receive an independent totals object.
func (s *LocalUsageStatistics) GetTodayStats(ctx context.Context, providerID int64) (*WindowStats, error) {
	stats, err := s.usageLogRepo.GetProviderTodayStats(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get today stats failed: %w", err)
	}

	return &WindowStats{
		Requests:     stats.Requests,
		Tokens:       stats.Tokens,
		Cost:         stats.Cost,
		StandardCost: stats.StandardCost,
		UserCost:     stats.UserCost,
	}, nil
}

// GetTodayStatsBatch returns copied totals for unique positive provider IDs
// using Today as the window start. Empty input yields an empty map, and
// successful batch results normalize missing or nil rows to zero totals.
//
// Absent or failed batch support falls back to at most eight concurrent reads.
// Individual errors, including cancellation observed by readers, are
// suppressed; the synchronized result map retains successful rows and fills
// failed rows with zeros after all callbacks return. The method therefore
// returns best-effort totals with nil error, leaving callers unable to
// distinguish those failed rows from observed empty usage.
func (s *LocalUsageStatistics) GetTodayStatsBatch(ctx context.Context, providerIDs []int64) (map[int64]*WindowStats, error) {
	uniqueIDs := make([]int64, 0, len(providerIDs))
	seen := make(map[int64]struct{}, len(providerIDs))
	for _, providerID := range providerIDs {
		if providerID <= 0 {
			continue
		}
		if _, exists := seen[providerID]; exists {
			continue
		}
		seen[providerID] = struct{}{}
		uniqueIDs = append(uniqueIDs, providerID)
	}

	result := make(map[int64]*WindowStats, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return result, nil
	}

	startTime := s.options.Today()
	if batchReader, ok := s.usageLogRepo.(LocalUsageStatsBatch); ok {
		statsByProvider, err := batchReader.GetProviderWindowStatsBatch(ctx, uniqueIDs, startTime)
		if err == nil {
			for _, providerID := range uniqueIDs {
				result[providerID] = normalizedLocalWindowStats(statsByProvider[providerID])
			}
			return result, nil
		}
	}

	// Wait for all best-effort reads before filling absent rows with zeros.
	// The mutex protects shared map writes while each callback independently
	// suppresses its read error, including cooperative cancellation.
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)

	for _, providerID := range uniqueIDs {
		id := providerID
		g.Go(func() error {
			stats, err := s.usageLogRepo.GetProviderWindowStats(gctx, id, startTime)
			if err != nil {
				return nil
			}
			mu.Lock()
			result[id] = normalizedLocalWindowStats(stats)
			mu.Unlock()
			return nil
		})
	}

	_ = g.Wait()

	for _, providerID := range uniqueIDs {
		if _, ok := result[providerID]; !ok {
			result[providerID] = &WindowStats{}
		}
	}
	return result, nil
}
