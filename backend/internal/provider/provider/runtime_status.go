package provider

import (
	"context"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// RuntimeStatusOptions binds batch and settings readers; the provider core determines query order.
func RuntimeStatusOptions(concurrency *scheduler.ConcurrencyService, usage interface {
	GetProviderWindowStats(context.Context, int64, time.Time) (*usage.ProviderStats, error)
}, sessions scheduler.SessionLimitCache, rpm scheduler.RPMCache, settings interface {
	GetOpenAIQuotaAutoPauseSettings(context.Context) providercore.QuotaAutoPauseSettings
},
) providercore.RuntimeStatusOptions {
	out := providercore.RuntimeStatusOptions{Now: time.Now}
	if concurrency != nil {
		out.Concurrency = concurrency.GetProviderConcurrencyBatch
	}
	if sessions != nil {
		out.Sessions = sessions.GetActiveSessionCountBatch
	}
	if rpm != nil {
		out.RPM = rpm.GetRPM
		out.RPMBatch = rpm.GetRPMBatch
	}
	if settings != nil {
		out.QuotaSettings = settings.GetOpenAIQuotaAutoPauseSettings
	}
	if usage != nil {
		out.WindowCost = func(ctx context.Context, id int64, start time.Time) (*float64, error) {
			v, err := usage.GetProviderWindowStats(ctx, id, start)
			if v == nil {
				return nil, err
			}
			cost := v.StandardCost
			return &cost, err
		}
	}
	return out
}
