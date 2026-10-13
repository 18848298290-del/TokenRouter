package provider

import (
	"context"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Administrative runtime status combines live load counters with local provider
// usage and dynamic quota settings. Optional readers remain absent when their
// services are absent, while a window-statistics result contributes
// StandardCost through a newly allocated float value to the provider core's
// display reader.

// RuntimeStatusOptions connects runtime counters and settings to provider
// status reads. Nil dependencies leave their callbacks unset; window usage
// returns StandardCost with the original error, or nil when no statistics were
// returned.
func RuntimeStatusOptions(concurrency *scheduler.ConcurrencyService, usage interface {
	// GetProviderWindowStats reads local provider usage since the supplied window
	// start.
	GetProviderWindowStats(context.Context, int64, time.Time) (*usage.ProviderStats, error)
}, sessions scheduler.SessionLimitCache, rpm scheduler.RPMCache, settings interface {
	// GetOpenAIQuotaAutoPauseSettings reads the current thresholds for OpenAI
	// quota display state.
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
