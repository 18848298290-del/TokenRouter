package provider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// LocalUsageStats 读取提供商展示所需的五个累计值，SQL 查询由 usage 存储适配器执行。
type LocalUsageStats interface {
	GetProviderWindowStats(context.Context, int64, time.Time) (*WindowStats, error)
	GetProviderTodayStats(context.Context, int64) (*WindowStats, error)
}
type LocalUsageStatsBatch interface {
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*WindowStats, error)
}

// LocalUsageStatsPair 在一次扫描中读取同一提供商的两个窗口。
type LocalUsageStatsPair interface {
	GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*WindowStats, *WindowStats, error)
}

type LocalUsageStatisticsOptions struct {
	Now   func() time.Time
	Today func() time.Time
	Log   func(string, ...any)
}

// LocalUsageStatistics 通过共享窗口缓存按需查询用量，处理并发回源和展示数据转换。
type LocalUsageStatistics struct {
	usageLogRepo LocalUsageStats
	cache        *OAuthUsageCache
	options      LocalUsageStatisticsOptions
}

func NewLocalUsageStatistics(reader LocalUsageStats, cache *OAuthUsageCache, options LocalUsageStatisticsOptions) *LocalUsageStatistics {
	return &LocalUsageStatistics{reader, cache, options}
}

// GetWindowPair 优先合并窗口，读取器不支持或查询失败时分别读取以保留部分结果。
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

// AddWindowStats 为 usage 数据添加窗口期统计
// 使用独立缓存（1 分钟），与 API 缓存分离。
func (s *LocalUsageStatistics) AddWindowStats(ctx context.Context, provider *Record, usage *UsageInfo) {
	// 修复：即使 FiveHour 为 nil，也要尝试获取统计数据
	// 因为 SevenDay/SevenDaySonnet/SevenDayFable 可能需要
	if usage.FiveHour == nil && usage.SevenDay == nil && usage.SevenDaySonnet == nil && usage.SevenDayFable == nil {
		return
	}

	// 检查窗口统计缓存（1 分钟）
	var windowStats *WindowStats
	if cache, ok := s.cache.LoadWindow(provider.ID); ok && s.options.Now().Sub(cache.Timestamp) < OAuthUsageWindowStatsCacheTTL {
		windowStats = cache.Stats
	}

	// 如果没有缓存，从数据库查询
	if windowStats == nil {
		// 使用统一的窗口开始时间计算逻辑（考虑窗口过期情况）
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

		// 缓存窗口统计（1 分钟）
		s.cache.StoreWindow(provider.ID, &OAuthWindowStatsCache{
			Stats:     windowStats,
			Timestamp: s.options.Now(),
		})
	}

	// 为 FiveHour 添加 WindowStats（5h 窗口统计）
	if usage.FiveHour != nil {
		usage.FiveHour.WindowStats = windowStats
	}
}

// GetTodayStats 获取提供商今日统计。
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

// GetTodayStatsBatch 批量获取提供商今日统计，优先走批量 SQL，失败时回退单提供商查询。
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
