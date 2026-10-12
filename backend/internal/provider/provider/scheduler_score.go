package provider

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// SchedulerScoreOptions binds shared scheduler feedback and effective scoring settings.
func SchedulerScoreOptions(concurrency *scheduler.ConcurrencyService, stats *scheduler.RuntimeStats, effective func(context.Context, *accessview.GroupConfig) policy.EffectiveSettings) provider.SchedulerScoreOptions {
	out := provider.SchedulerScoreOptions{Warn: slog.Warn, Score: func(ctx context.Context, group *accessview.GroupConfig, values []*provider.Record, load map[int64]*provider.SchedulerLoad) map[int64]provider.ProviderSchedulerScore {
		projected := make([]*scheduler.ScoreProvider, len(values))
		sources := make(map[*scheduler.ScoreProvider]*provider.Record, len(values))
		for i, value := range values {
			if value != nil {
				v := &scheduler.ScoreProvider{ID: value.ID, Platform: value.Platform, Priority: value.Priority, SessionWindowEnd: value.SessionWindowEnd}
				projected[i] = v
				sources[v] = value
			}
		}
		loads := make(map[int64]*scheduler.ProviderLoadInfo, len(load))
		for id, v := range load {
			if v != nil {
				loads[id] = &scheduler.ProviderLoadInfo{ProviderID: v.ProviderID, CurrentConcurrency: v.CurrentConcurrency, WaitingCount: v.WaitingCount, LoadRate: v.LoadRate}
			}
		}
		var g *scheduler.ScoreGroup
		if group != nil {
			g = &scheduler.ScoreGroup{}
		}
		settings := effective(ctx, group)
		scores := scheduler.BuildScoreSnapshot(projected, loads, stats, g, settings.Weights, settings.StickyWeightedEnabled, func(v *scheduler.ScoreProvider, now time.Time) float64 {
			return provider.OpenAIQuotaHeadroomFactor(sources[v], now)
		}, time.Now())
		result := make(map[int64]provider.ProviderSchedulerScore, len(scores))
		for id, v := range scores {
			result[id] = provider.ProviderSchedulerScore{BaseScore: v.BaseScore, StickyScore: v.StickyScore, StickyScoreInfinity: v.StickyScoreInfinity, StickyWeightedEnabled: v.StickyWeightedEnabled}
		}
		return result
	}}
	if concurrency != nil {
		out.Load = func(ctx context.Context, values []provider.SchedulerLoadRequest) (map[int64]*provider.SchedulerLoad, error) {
			requests := make([]scheduler.ProviderWithConcurrency, len(values))
			for i, v := range values {
				requests[i] = scheduler.ProviderWithConcurrency{ID: v.ID, MaxConcurrency: v.MaxConcurrency}
			}
			loads, err := concurrency.GetProvidersLoadBatch(ctx, requests)
			if loads == nil {
				return nil, err
			}
			result := make(map[int64]*provider.SchedulerLoad, len(loads))
			for id, v := range loads {
				if v != nil {
					result[id] = &provider.SchedulerLoad{ProviderID: v.ProviderID, CurrentConcurrency: v.CurrentConcurrency, WaitingCount: v.WaitingCount, LoadRate: v.LoadRate}
				}
			}
			return result, err
		}
	}
	return out
}
