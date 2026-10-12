package testkit

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type windowCosts struct{ source usage.UsageLogRepository }

type windowCostBatch interface {
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usage.ProviderStats, error)
}
type windowCostsBatch struct {
	windowCosts
	batch windowCostBatch
}

// WindowCosts adapts usage fixtures, retaining optional batch lookup and nil results.
func WindowCosts(source usage.UsageLogRepository) billing.WindowCostSource {
	if source == nil {
		return nil
	}
	base := windowCosts{source}
	if batch, ok := source.(windowCostBatch); ok {
		return windowCostsBatch{windowCosts: base, batch: batch}
	}
	return base
}

func (s windowCosts) GetWindow(ctx context.Context, id int64, start time.Time) (*billing.WindowCostStats, error) {
	value, err := s.source.GetProviderWindowStats(ctx, id, start)
	if value == nil {
		return nil, err
	}
	return &billing.WindowCostStats{StandardCost: value.StandardCost}, err
}

func (s windowCostsBatch) GetWindows(ctx context.Context, ids []int64, start time.Time) (map[int64]*billing.WindowCostStats, error) {
	values, err := s.batch.GetProviderWindowStatsBatch(ctx, ids, start)
	if values == nil {
		return nil, err
	}
	out := make(map[int64]*billing.WindowCostStats, len(values))
	for id, value := range values {
		if value == nil {
			out[id] = nil
		} else {
			out[id] = &billing.WindowCostStats{StandardCost: value.StandardCost}
		}
	}
	return out, err
}
