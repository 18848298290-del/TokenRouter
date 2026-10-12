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

// capacityProviders converts provider records to routing capacity data.
type capacityProviders struct {
	Store    *providerpostgres.ProviderStore
	Settings func(context.Context) provider.QuotaAutoPauseSettings
}

// provideGroupCapacity binds provider storage and concurrency, session, and RPM readers with dynamic quota settings.
func provideGroupCapacity(providers *providerpostgres.ProviderStore, groups *routingpostgres.GroupStore, concurrency *scheduler.ConcurrencyService, sessions scheduler.SessionLimitCache, rpm scheduler.RPMCache, settings *provider.QuotaSettingsCache) *routing.CapacityService {
	return routing.NewCapacityService(capacityProviders{Store: providers, Settings: func(ctx context.Context) provider.QuotaAutoPauseSettings {
		return settings.GetOpenAIQuotaAutoPauseSettings(ctx)
	}}, groups, concurrency, sessions, rpm)
}

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
