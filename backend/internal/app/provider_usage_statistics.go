package app

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type providerLocalStats struct{ source usage.UsageLogRepository }

// providerLocalStatsPairSource queries provider usage for two windows together.
type providerLocalStatsPairSource interface {
	GetProviderWindowStatsPair(context.Context, int64, time.Time, time.Time) (*usage.ProviderStats, *usage.ProviderStats, error)
}

type providerLocalStatsBatchSource interface {
	GetProviderWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usage.ProviderStats, error)
}
type providerLocalStatsBatch struct {
	providerLocalStats
	batch providerLocalStatsBatchSource
}

// GetProviderWindowStatsPair returns an error when combined queries are unavailable, allowing the caller to query each window.
func (r providerLocalStats) GetProviderWindowStatsPair(ctx context.Context, id int64, first, second time.Time) (*provider.WindowStats, *provider.WindowStats, error) {
	reader, ok := r.source.(providerLocalStatsPairSource)
	if !ok {
		return nil, nil, errors.New("用量读取器不支持双窗口查询")
	}
	a, b, err := reader.GetProviderWindowStatsPair(ctx, id, first, second)
	return localWindowStats(a), localWindowStats(b), err
}

func localWindowStats(v *usage.ProviderStats) *provider.WindowStats {
	if v == nil {
		return nil
	}
	return &provider.WindowStats{Requests: v.Requests, Tokens: v.Tokens, Cost: v.Cost, StandardCost: v.StandardCost, UserCost: v.UserCost}
}

func (r providerLocalStats) GetProviderWindowStats(ctx context.Context, id int64, start time.Time) (*provider.WindowStats, error) {
	v, err := r.source.GetProviderWindowStats(ctx, id, start)
	return localWindowStats(v), err
}

func (r providerLocalStats) GetProviderTodayStats(ctx context.Context, id int64) (*provider.WindowStats, error) {
	v, err := r.source.GetProviderTodayStats(ctx, id)
	return localWindowStats(v), err
}

func (r providerLocalStatsBatch) GetProviderWindowStatsBatch(ctx context.Context, ids []int64, start time.Time) (map[int64]*provider.WindowStats, error) {
	values, err := r.batch.GetProviderWindowStatsBatch(ctx, ids, start)
	if values == nil {
		return nil, err
	}
	out := make(map[int64]*provider.WindowStats, len(values))
	for id, v := range values {
		out[id] = localWindowStats(v)
	}
	return out, err
}

// newProviderLocalUsageStats exposes supported usage queries; the provider module handles batch failures.
func newProviderLocalUsageStats(source usage.UsageLogRepository) provider.LocalUsageStats {
	reader := providerLocalStats{source}
	if batch, ok := source.(providerLocalStatsBatchSource); ok {
		return providerLocalStatsBatch{reader, batch}
	}
	return reader
}
