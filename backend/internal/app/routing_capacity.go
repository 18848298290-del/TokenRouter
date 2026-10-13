package app

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Group capacity reads narrow provider records before combining concurrency,
// session, and RPM observations in routing. Quota settings are captured once
// for each nonempty provider read, while each provider capacity conversion
// evaluates its windows using the current observation time.

// capacityProviders adapts stored provider capacity with current quota pause
// settings.
type capacityProviders struct {
	Store *providerpostgres.ProviderStore // stored capacity candidates
	// Settings is required when a provider read returns any rows. The adapter
	// reads this callback once and uses its policy for the whole result.
	Settings func(context.Context) provider.QuotaAutoPauseSettings // current quota policy
}

// provideGroupCapacity binds provider capacity reads and shared
// concurrency/session/RPM observations. The settings callback reads current
// OpenAI quota pause policy, leaving capacity calculation and missing
// observations to routing.CapacityService.
func provideGroupCapacity(providers *providerpostgres.ProviderStore, groups *routingpostgres.GroupStore, concurrency *scheduler.ConcurrencyService, sessions scheduler.SessionLimitCache, rpm scheduler.RPMCache, settings *provider.QuotaSettingsCache) *routing.CapacityService {
	return routing.NewCapacityService(capacityProviders{Store: providers, Settings: func(ctx context.Context) provider.QuotaAutoPauseSettings {
		return settings.GetOpenAIQuotaAutoPauseSettings(ctx)
	}}, groups, concurrency, sessions, rpm)
}

// ListSchedulableByGroupID returns observed capacity for the group's stored
// providers. Repository failure stops conversion, empty rows return nil, and
// one settings read supplies the quota policy used across that result.
func (r capacityProviders) ListSchedulableByGroupID(ctx context.Context, id int64) ([]provider.CapacitySnapshot, error) {
	values, err := r.Store.ListSchedulableByGroupID(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	settings := r.Settings(ctx)
	out := make([]provider.CapacitySnapshot, len(values))
	for i, v := range values {
		out[i] = provider.ProjectObservedCapacity(provider.GroupProviderCapacityRow{ProviderID: v.ID, Platform: v.Platform, Concurrency: v.Concurrency, Extra: v.Extra, SessionWindowStart: v.SessionWindowStart, SessionWindowEnd: v.SessionWindowEnd}, settings, time.Now())
	}
	return out, nil
}

// ListSchedulableCapacityByGroupIDs returns capacity rows for the requested
// group IDs. Empty rows return nil, while each converted row retains its source
// group identity and uses one policy read for the batch.
func (r capacityProviders) ListSchedulableCapacityByGroupIDs(ctx context.Context, ids []int64) ([]routing.CapacityProviderRow, error) {
	values, err := r.Store.ListSchedulableCapacityByGroupIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	settings := r.Settings(ctx)
	out := make([]routing.CapacityProviderRow, len(values))
	for i, v := range values {
		out[i] = routing.CapacityProviderRow{GroupID: v.GroupID, Provider: provider.ProjectObservedCapacity(v, settings, time.Now())}
	}
	return out, nil
}
