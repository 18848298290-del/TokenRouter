package app

import (
	"context"

	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Gateway execution reads provider configuration through the application's
// provider store and sends quota increments to billing storage. Read adapters
// create execution targets with provider-module copying and clock setup,
// retaining nil list results so selection sees the underlying query's absence
// convention.

// executionProviderStore adapts provider reads and quota writes for gateway
// execution.
type executionProviderStore struct {
	// usage handles funding counters while data handles configuration and health.
	// Application assembly supplies both shared stores, keeping request reads and accounting writes on their owning APIs.
	usage *billingpostgres.ProviderUsageStore // provider quota writer
	data  *providerpostgres.ProviderStore     // provider configuration store
}

// GetByID converts the stored record into a copied execution target. Missing
// records remain nil through NewExecutionProvider, and the repository error is
// returned with the converted result.
func (r *executionProviderStore) GetByID(ctx context.Context, id int64) (*gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.GetByID(ctx, id)
	return gatewayprovider.NewExecutionProvider(v), err
}

// GetByIDs converts requested provider records into copied execution targets.
// Nil repository slices remain nil, while each available element follows
// NewExecutionProvider's copying and clock rules; any repository error is
// retained.
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

// ListByPlatform converts stored platform records into gateway execution
// targets.
func (r *executionProviderStore) ListByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListByPlatform(ctx, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulable converts stored scheduling candidates into gateway execution
// targets.
func (r *executionProviderStore) ListSchedulable(ctx context.Context) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulable(ctx)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableProviderLoads returns candidate IDs and their maximum
// concurrency for scheduler reads. It stops on repository error and otherwise
// creates rows from the store's lightweight load data.
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

// ListSchedulableByGroupID converts eligible group providers into gateway
// execution targets.
func (r *executionProviderStore) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByGroupID(ctx, groupID)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableCapacityByGroupIDs delegates lightweight provider capacity
// reads for the requested groups.
func (r *executionProviderStore) ListSchedulableCapacityByGroupIDs(ctx context.Context, groupIDs []int64) ([]providercore.GroupProviderCapacityRow, error) {
	return r.data.ListSchedulableCapacityByGroupIDs(ctx, groupIDs)
}

// ListSchedulableByPlatform converts eligible platform providers into gateway
// execution targets.
func (r *executionProviderStore) ListSchedulableByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByPlatform(ctx, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableByGroupIDAndPlatform converts eligible group and platform
// provider records.
func (r *executionProviderStore) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableByPlatforms converts providers eligible for the supplied
// platform list.
func (r *executionProviderStore) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByPlatforms(ctx, platforms)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableUngroupedByPlatform converts eligible ungrouped providers of
// one platform.
func (r *executionProviderStore) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableUngroupedByPlatform(ctx, platform)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableUngroupedByPlatforms converts eligible ungrouped providers of
// the requested platforms.
func (r *executionProviderStore) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableUngroupedByPlatforms(ctx, platforms)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListSchedulableByGroupIDAndPlatforms converts eligible providers for one
// group and platform list.
func (r *executionProviderStore) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, platforms)
	return gatewayprovider.ExecutionProviders(v), err
}

// ListModelAvailabilityCandidates converts persistent providers selected for
// model diagnostics. groupID, platforms, and includeGrouped are passed to
// storage, whose candidate rules include configured providers beyond
// immediately schedulable execution targets.
func (r *executionProviderStore) ListModelAvailabilityCandidates(
	ctx context.Context,
	groupID *int64,
	platforms []string,
	includeGrouped bool,
) ([]gatewayprovider.ExecutionProvider, error) {
	v, err := r.data.ListModelAvailabilityCandidates(ctx, groupID, platforms, includeGrouped)
	return gatewayprovider.ExecutionProviders(v), err
}
