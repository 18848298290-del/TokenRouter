package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// LocalUsageStats reads the five local usage totals shown for a provider.
type LocalUsageStats interface {
	GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error)
	GetProviderTodayStats(context.Context, int64) (*WindowStats, error)
}
type LocalUsageStatsBatch interface {
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*WindowStats, error)
}

// LocalUsageStatsPair reads two windows for the same provider in one query.
type LocalUsageStatsPair interface {
	GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*WindowStats, *WindowStats, error)
}

type LocalUsageStatisticsOptions struct {
	Now   func() time.Time
	Today func() time.Time
	Log   func(string, ...any)
}

// LocalUsageStatistics queries and caches local provider usage for display.
type LocalUsageStatistics struct {
	usageLogRepo LocalUsageStats
	cache        *OAuthUsageCache
	options      LocalUsageStatisticsOptions
}

func NewLocalUsageStatistics(reader LocalUsageStats, cache *OAuthUsageCache, options LocalUsageStatisticsOptions) *LocalUsageStatistics {
	return &LocalUsageStatistics{reader, cache, options}
}

// GetWindowPair tries a combined query, then separate queries to retain partial results if the reader cannot
// combine them.
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

func normalizedLocalWindowStats(value *WindowStats) *WindowStats {
	if value == nil {
		return &WindowStats{}
	}
	return clonePointer(value)
}

// AddWindowStats attaches local session-window statistics to FiveHour when present.
// Statistics use a separate one-minute cache.
func (s *LocalUsageStatistics) AddWindowStats(ctx context.Context, provider *Record, usage *UsageInfo) {
	// Query statistics whenever any supported window exists.
	// Attachment below is limited to the five-hour window.
	if usage.FiveHour == nil && usage.SevenDay == nil && usage.SevenDaySonnet == nil && usage.SevenDayFable == nil {
		return
	}

	var windowStats *WindowStats
	if cache, ok := s.cache.LoadWindow(provider.ID); ok && s.options.Now().Sub(cache.Timestamp) < OAuthUsageWindowStatsCacheTTL {
		windowStats = cache.Stats
	}

	if windowStats == nil {
		// Derive the current window start, accounting for an expired session window.
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

// GetTodayStats returns today's local provider usage totals.
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

// GetTodayStatsBatch tries one batch query, then per-provider queries; failed individual reads yield zero
// totals.
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
