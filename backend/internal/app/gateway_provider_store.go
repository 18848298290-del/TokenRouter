package app

import (
	"context"

	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// executionProviderStore adapts provider records and billing usage writes for gateway execution.
type executionProviderStore struct {
	usage *billingpostgres.ProviderUsageStore
	data  *providerpostgres.ProviderStore
}

func (r *executionProviderStore) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.GetByID(ctx, id)
	return gatewayprovider.NewExecutionProvider(v), err
}

func (r *executionProviderStore) GetByIDs(ctx context.Context, ids []int64) ([]*gatewayprovider.ExecutionProvider, error) {
	values, err := r.data.GetByIDs(ctx, ids)
	if values == nil {
		return nil, err
	}
	out := make([]*gatewayprovider.ExecutionProvider, len(values))
	for i := range values {
		out[i] = gatewayprovider.NewExecutionProvider(values[i])
	}
	return out, err
}

func (r *executionProviderStore) ListByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListByPlatform(ctx, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulable(ctx context.Context) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulable(ctx)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableProviderLoads(ctx context.Context) ([]scheduler.ProviderWithConcurrency, error) {
	rows, err := r.data.ListSchedulableProviderLoads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.ProviderWithConcurrency, len(rows))
	for i, v := range rows {
		out[i] = scheduler.ProviderWithConcurrency{ID: v.ID, MaxConcurrency: v.MaxConcurrency}
	}
	return out, nil
}

func (r *executionProviderStore) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByGroupID(ctx, groupID)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableCapacityByGroupIDs(ctx context.Context, groupIDs []int64) ([]providercore.GroupProviderCapacityRow, error) {
	return r.data.ListSchedulableCapacityByGroupIDs(ctx, groupIDs)
}

func (r *executionProviderStore) ListSchedulableByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByPlatform(ctx, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByPlatforms(ctx, platforms)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableUngroupedByPlatform(ctx, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableUngroupedByPlatforms(ctx, platforms)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, platforms)
	return gatewayprovider.ExecutionProviders(v), err
}

func (r *executionProviderStore) ListModelAvailabilityCandidates(
	ctx context.Context,
	groupID *int64,
	platforms []string,
	includeGrouped bool,
) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListModelAvailabilityCandidates(ctx, groupID, platforms, includeGrouped)
	return gatewayprovider.ExecutionProviders(v), err
}
