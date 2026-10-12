package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// selectionFreeQuotaGates holds separate free-quota caches for generic and compatible selection,
// and a cache factory for advanced selectors.
type selectionFreeQuotaGates struct {
	Generic    *provider.FreeQuotaGate
	Compatible *provider.FreeQuotaGate
	Advanced   func() *provider.FreeQuotaGate
}

// provideSelectionFreeQuota binds quota configuration and usage reads to gates that own their caches and refresh tasks.
func provideSelectionFreeQuota(cfg *config.Config, reader usage.UsageLogRepository, tasks *lifecycle.Tasks) *selectionFreeQuotaGates {
	metrics := &provider.FreeQuotaMetrics{}
	options := func() provider.FreeQuotaOptions {
		if cfg == nil {
			return provider.FreeQuotaOptions{}
		}
		v := cfg.Gateway.Grok
		return provider.FreeQuotaOptions{
			Enabled: v.FreeQuotaSoftGateEnabled, TokenLimit: v.FreeQuotaTokenLimit,
			Percent: v.FreeQuotaSoftGatePercent, WindowHours: v.FreeQuotaWindowHours, CacheSeconds: v.FreeQuotaStatsCacheSeconds,
		}
	}
	var load func(context.Context, []int64, time.Time) (map[int64]int64, error)
	if reader != nil {
		load = func(ctx context.Context, ids []int64, start time.Time) (map[int64]int64, error) {
			return usage.ReadProviderTokenWindow(ctx, reader, ids, start)
		}
	}
	factory := func() *provider.FreeQuotaGate {
		return provider.NewFreeQuotaGate(options, load, tasks.Go, time.Now, func(failed bool, message string, fields ...any) {
			if failed {
				slog.Warn(message, fields...)
			} else {
				slog.Info(message, fields...)
			}
		}, metrics)
	}
	// Each advanced selector receives its own cache, as do the generic and compatible selectors.
	return &selectionFreeQuotaGates{Generic: factory(), Compatible: factory(), Advanced: factory}
}
