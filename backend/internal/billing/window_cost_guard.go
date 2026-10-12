package billing

import (
	"context"
	"maps"
	"sync/atomic"
	"time"
)

var sharedWindowCostMetrics WindowCostMetrics

// WindowCostCache stores standard costs for provider five-hour windows.
// Cache keys use window_cost:provider:{providerID}.
type WindowCostCache interface {
	// GetWindowCost returns the cached cost and whether the key was found.
	// A hit returns (cost, true, nil).
	// A miss returns (0, false, nil).
	// A cache error returns (0, false, err).
	GetWindowCost(ctx context.Context, providerID int64) (cost float64, hit bool, err error)

	// SetWindowCost caches the provider's window cost.
	SetWindowCost(ctx context.Context, providerID int64, cost float64) error

	// GetWindowCostBatch returns cached costs indexed by provider ID.
	// Providers missing from the cache are omitted.
	GetWindowCostBatch(ctx context.Context, providerIDs []int64) (map[int64]float64, error)
}

// CostWindowInput contains a provider's cost window, limit, and sticky-session reserve.
type CostWindowInput struct {
	ID             int64
	Enabled        bool
	Limit, Reserve float64
	Start, End     *time.Time
}

type (
	WindowCostStats  struct{ StandardCost float64 }
	WindowCostSource interface {
		GetWindow(context.Context, int64, time.Time) (*WindowCostStats, error)
	}
)

type WindowCostBatchSource interface {
	GetWindows(context.Context, []int64, time.Time) (map[int64]*WindowCostStats, error)
}

type WindowCostMetrics struct{ Hit, Miss, BatchSQL, Fallback, Errors atomic.Int64 }

type WindowCostGuardOptions struct {
	Now   func() time.Time
	Log   func(string, ...any)
	Debug func(string, ...any)
	Stats *WindowCostMetrics
}

// WindowCostGuard reads standard window costs through its cache and query interfaces.
type WindowCostGuard struct {
	cache  WindowCostCache
	source WindowCostSource
	now    func() time.Time
	log    func(string, ...any)
	debug  func(string, ...any)
	stats  *WindowCostMetrics
}

type windowCostPrefetchKey struct{}

func SharedWindowCostMetrics() *WindowCostMetrics { return &sharedWindowCostMetrics }

func NewWindowCostGuard(cache WindowCostCache, source WindowCostSource, options WindowCostGuardOptions) *WindowCostGuard {
	return &WindowCostGuard{cache: cache, source: source, now: options.Now, log: options.Log, debug: options.Debug, stats: options.Stats}
}

func WithPrefetchedWindowCosts(ctx context.Context, costs map[int64]float64) context.Context {
	return context.WithValue(ctx, windowCostPrefetchKey{}, maps.Clone(costs))
}

func PrefetchedWindowCost(ctx context.Context, id int64) (float64, bool) {
	if ctx == nil || id <= 0 {
		return 0, false
	}
	m, ok := ctx.Value(windowCostPrefetchKey{}).(map[int64]float64)
	if !ok || len(m) == 0 {
		return 0, false
	}
	v, found := m[id]
	return v, found
}

func (s *WindowCostGuard) Prefetch(ctx context.Context, providers []CostWindowInput) context.Context {
	if ctx == nil || len(providers) == 0 || s.cache == nil || s.source == nil {
		return ctx
	}

	providerByID := make(map[int64]*CostWindowInput)
	providerIDs := make([]int64, 0, len(providers))
	for i := range providers {
		provider := &providers[i]
		if provider == nil || !provider.Enabled {
			continue
		}
		if provider.Limit <= 0 {
			continue
		}
		providerByID[provider.ID] = provider
		providerIDs = append(providerIDs, provider.ID)
	}
	if len(providerIDs) == 0 {
		return ctx
	}

	costs := make(map[int64]float64, len(providerIDs))
	cacheValues, err := s.cache.GetWindowCostBatch(ctx, providerIDs)
	if err == nil {
		maps.Copy(costs, cacheValues)
		s.stats.Hit.Add(int64(len(cacheValues)))
	} else {
		s.stats.Errors.Add(1)
		s.log("window_cost batch cache read failed: %v", err)
	}
	cacheMissCount := max(len(providerIDs)-len(costs), 0)
	s.stats.Miss.Add(int64(cacheMissCount))

	missingByStart := make(map[int64][]int64)
	startTimes := make(map[int64]time.Time)
	for _, providerID := range providerIDs {
		if _, ok := costs[providerID]; ok {
			continue
		}
		provider := providerByID[providerID]
		if provider == nil {
			continue
		}
		startTime := CurrentCostWindowStart(provider.Start, provider.End, s.now())
		startKey := startTime.Unix()
		missingByStart[startKey] = append(missingByStart[startKey], providerID)
		startTimes[startKey] = startTime
	}
	if len(missingByStart) == 0 {
		return WithPrefetchedWindowCosts(ctx, costs)
	}

	batchReader, hasBatch := s.source.(WindowCostBatchSource)
	for startKey, ids := range missingByStart {
		startTime := startTimes[startKey]

		if hasBatch {
			s.stats.BatchSQL.Add(1)
			queryStart := s.now()
			statsByProvider, err := batchReader.GetWindows(ctx, ids, startTime)
			if err == nil {
				s.debug("window_cost_batch_query_ok",
					"providers", len(ids),
					"window_start", startTime.Format(time.RFC3339),
					"duration_ms", s.now().Sub(queryStart).Milliseconds())
				for _, providerID := range ids {
					stats := statsByProvider[providerID]
					cost := 0.0
					if stats != nil {
						cost = stats.StandardCost
					}
					costs[providerID] = cost
					_ = s.cache.SetWindowCost(ctx, providerID, cost)
				}
				continue
			}
			s.stats.Errors.Add(1)
			s.log("window_cost batch db query failed: start=%s err=%v", startTime.Format(time.RFC3339), err)
		}

		// Query providers individually when batch reads are unavailable or fail; leave failed reads uncached.
		s.stats.Fallback.Add(int64(len(ids)))
		for _, providerID := range ids {
			stats, err := s.source.GetWindow(ctx, providerID, startTime)
			if err != nil {
				s.stats.Errors.Add(1)
				continue
			}
			cost := stats.StandardCost
			costs[providerID] = cost
			_ = s.cache.SetWindowCost(ctx, providerID, cost)
		}
	}

	return WithPrefetchedWindowCosts(ctx, costs)
}

func (s *WindowCostGuard) Allow(ctx context.Context, provider CostWindowInput, isSticky bool) bool {
	// The caller determines whether this provider is subject to the cost limit.
	if !provider.Enabled {
		return true
	}

	limit := provider.Limit
	if limit <= 0 {
		return true // Nonpositive limits are disabled.
	}

	// Prefer prefetched costs, then the shared cache.
	var currentCost float64
	if cost, ok := PrefetchedWindowCost(ctx, provider.ID); ok {
		currentCost = cost
		goto checkSchedulability
	}
	if s.cache != nil {
		if cost, hit, err := s.cache.GetWindowCost(ctx, provider.ID); err == nil && hit {
			currentCost = cost
			goto checkSchedulability
		}
	}

	// Read storage when neither cache supplies a cost.
	{
		// Expired windows begin at the current local hour.
		startTime := CurrentCostWindowStart(provider.Start, provider.End, s.now())

		stats, err := s.source.GetWindow(ctx, provider.ID, startTime)
		if err != nil {
			// Allow scheduling when the cost query fails.
			return true
		}

		// Standard cost excludes the provider multiplier.
		currentCost = stats.StandardCost

		// Cache write failures do not affect admission.
		if s.cache != nil {
			_ = s.cache.SetWindowCost(ctx, provider.ID, currentCost)
		}
	}

checkSchedulability:
	schedulability := CheckWindowCost(currentCost, provider.Limit, provider.Reserve)

	switch schedulability {
	case WindowCostSchedulable:
		return true
	case WindowCostStickyOnly:
		return isSticky
	case WindowCostNotSchedulable:
		return false
	}
	return true
}
