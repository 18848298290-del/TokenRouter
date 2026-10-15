package app

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	usagepostgres "github.com/TokenFlux/TokenRouter/internal/usage/postgres"
)

// usage_window_stats.go adapts stored provider window totals for billing
// admission checks.
// Anthropic window-cost selection uses standard usage cost rather than user
// charge or provider cost. The adapter keeps absent totals as nil for both
// single and batch reads, returning only the StandardCost field consumed by
// billing.WindowCostGuard alongside source errors.

// usageWindowStats narrows persisted provider totals for billing window-cost
// admission.
type usageWindowStats struct {
	store *usagepostgres.Store // provider window totals
}

// GetWindow returns the provider's standard cost from the supplied window
// start. Absent totals remain nil, while an available cost result is returned
// alongside the usage store's error.
func (s usageWindowStats) GetWindow(ctx context.Context, id int64, start time.Time) (*billing.WindowCostStats, error) {
	v, e := s.store.GetProviderWindowStats(ctx, id, start)
	if v == nil {
		return nil, e
	}
	return &billing.WindowCostStats{StandardCost: v.StandardCost}, e
}

// GetWindows narrows batch totals to standard cost while retaining nil maps and
// entries. It allocates a new result map for available rows and returns the
// original query error with those conversions.
func (s usageWindowStats) GetWindows(ctx context.Context, ids []int64, start time.Time) (map[int64]*billing.WindowCostStats, error) {
	v, e := s.store.GetProviderWindowStatsBatch(ctx, ids, start)
	if v == nil {
		return nil, e
	}
	out := make(map[int64]*billing.WindowCostStats, len(v))
	for id, row := range v {
		if row == nil {
			out[id] = nil
		} else {
			out[id] = &billing.WindowCostStats{StandardCost: row.StandardCost}
		}
	}
	return out, e
}
