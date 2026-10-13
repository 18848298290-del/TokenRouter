package app

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	routingpostgres "github.com/TokenFlux/TokenRouter/internal/routing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/rediscache/codec"
)

// Scheduler snapshots read full provider records through codec wrappers and
// narrow group records to hydration and status. These adapters retain nil query
// results and pass repository errors alongside converted data, leaving snapshot
// rebuilding, copying, and storage encoding to the scheduler and codec.

// schedulerProviderSource wraps stored provider reads for scheduler snapshot
// persistence.
type schedulerProviderSource struct {
	*providerpostgres.ProviderStore // shared provider snapshot reads
}

// schedulerGroupSource narrows routing group reads for scheduler snapshot
// state.
type schedulerGroupSource struct {
	*routingpostgres.GroupStore // shared group snapshot reads
}

// GetByID wraps the current stored provider for scheduler snapshot conversion.
func (r schedulerProviderSource) GetByID(ctx context.Context, id int64) (scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.GetByID(ctx, id)
	return codec.WrapRecord(value), err
}

// GetByIDs wraps requested provider records for batched scheduler snapshot
// conversion.
func (r schedulerProviderSource) GetByIDs(ctx context.Context, ids []int64) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.GetByIDs(ctx, ids)
	return wrapSchedulerRecordPointers(value), err
}

// ListSchedulableByPlatform wraps eligible provider records for one platform
// snapshot read.
func (r schedulerProviderSource) ListSchedulableByPlatform(ctx context.Context, platform string) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.ListSchedulableByPlatform(ctx, platform)
	return wrapSchedulerRecords(value), err
}

// ListSchedulableUngroupedByPlatform wraps eligible ungrouped providers for one
// platform.
func (r schedulerProviderSource) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.ListSchedulableUngroupedByPlatform(ctx, platform)
	return wrapSchedulerRecords(value), err
}

// ListSchedulableByPlatforms wraps eligible providers from the requested
// platform list.
func (r schedulerProviderSource) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.ListSchedulableByPlatforms(ctx, platforms)
	return wrapSchedulerRecords(value), err
}

// ListSchedulableUngroupedByPlatforms wraps eligible ungrouped providers from
// the platform list.
func (r schedulerProviderSource) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.ListSchedulableUngroupedByPlatforms(ctx, platforms)
	return wrapSchedulerRecords(value), err
}

// ListSchedulableByGroupIDAndPlatform wraps eligible providers for a group and
// platform.
func (r schedulerProviderSource) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.ListSchedulableByGroupIDAndPlatform(ctx, group, platform)
	return wrapSchedulerRecords(value), err
}

// ListSchedulableByGroupIDAndPlatforms wraps eligible group providers from the
// requested platforms.
func (r schedulerProviderSource) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, group int64, platforms []string) ([]scheduler.SnapshotProvider, error) {
	value, err := r.ProviderStore.ListSchedulableByGroupIDAndPlatforms(ctx, group, platforms)
	return wrapSchedulerRecords(value), err
}

// schedulerSnapshotGroup returns group identity, status, and hydration or nil
// for absence. The selected scalar fields form the group snapshot used by
// scheduler storage and candidate reads.
func schedulerSnapshotGroup(value *routing.Group) *scheduler.SnapshotGroup {
	if value == nil {
		return nil
	}
	return &scheduler.SnapshotGroup{ID: value.ID, Name: value.Name, Status: value.Status, Hydrated: value.Hydrated}
}

// GetByID narrows the full stored routing group into a scheduler snapshot.
func (r schedulerGroupSource) GetByID(ctx context.Context, id int64) (*scheduler.SnapshotGroup, error) {
	value, err := r.GroupStore.GetByID(ctx, id)
	return schedulerSnapshotGroup(value), err
}

// GetByIDLite narrows the lightweight routing group into a scheduler snapshot.
func (r schedulerGroupSource) GetByIDLite(ctx context.Context, id int64) (*scheduler.SnapshotGroup, error) {
	value, err := r.GroupStore.GetByIDLite(ctx, id)
	return schedulerSnapshotGroup(value), err
}

// ListActive returns narrowed snapshots of active routing groups and retains
// nil results.
func (r schedulerGroupSource) ListActive(ctx context.Context) ([]scheduler.SnapshotGroup, error) {
	value, err := r.GroupStore.ListActive(ctx)
	if value == nil {
		return nil, err
	}
	out := make([]scheduler.SnapshotGroup, len(value))
	for i := range value {
		out[i] = *schedulerSnapshotGroup(&value[i])
	}
	return out, err
}

// ListActiveIDs delegates active routing group identities for scheduler rebuild
// enumeration.
func (r schedulerGroupSource) ListActiveIDs(ctx context.Context) ([]int64, error) {
	return r.GroupStore.ListActiveIDs(ctx)
}

// wrapSchedulerRecords creates codec wrappers around each source record and
// retains nil input. Wrappers reference elements of values, so consumers keep
// those records stable while scheduler conversion or encoding reads them.
func wrapSchedulerRecords(values []provider.Record) []scheduler.SnapshotProvider {
	if values == nil {
		return nil
	}
	out := make([]scheduler.SnapshotProvider, len(values))
	for i := range values {
		out[i] = codec.WrapRecord(&values[i])
	}
	return out
}
