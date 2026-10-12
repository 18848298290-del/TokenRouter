package selection

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// advancedSchedulerEffectiveSettingsForGroup applies group overrides to global runtime settings.
func (s *Compatible) advancedSchedulerEffectiveSettingsForGroup(ctx context.Context, group *routing.Group) policy.EffectiveSettings {
	var parameters *schedulercore.Parameters
	if s != nil {
		parameters = s.schedulerParameters
	}
	return parameters.Effective(ctx, schedulerGroupOverrides(group))
}

// advancedSchedulerEffectiveSettingsForRequest resolves the final group and returns request settings.
// A missing group uses global settings.
func (s *Compatible) advancedSchedulerEffectiveSettingsForRequest(
	ctx context.Context,
	groupID *int64,
) policy.EffectiveSettings {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.advancedSchedulerEffectiveSettingsForGroup(ctx, s.advancedSchedulerGroupForRequest(ctx, groupID))
}

// advancedSchedulerGroupForRequest reuses the request group or reads it when a scheduling snapshot is configured.
func (s *Compatible) advancedSchedulerGroupForRequest(ctx context.Context, id *int64) *routing.Group {
	return schedulerRequestGroup(ctx, id, s != nil && s.schedulerSnapshot != nil, s.readSchedulingGroup)
}

// schedulerGroupOverrides returns advanced-group overrides and an empty override for basic or missing groups.
func schedulerGroupOverrides(group *routing.Group) policy.GroupAdvancedSchedulerOverrides {
	if group != nil && group.UsesAdvancedScheduler() {
		return group.AdvancedSchedulerOverrides
	}
	return policy.GroupAdvancedSchedulerOverrides{}
}

// schedulerRequestGroup prefers a valid request group and permits a repository read when snapshots are configured.
func schedulerRequestGroup(ctx context.Context, id *int64, snapshotAvailable bool, read func(context.Context, int64) (*routing.Group, error)) *routing.Group {
	if id == nil || *id <= 0 {
		return nil
	}
	if ctx != nil {
		if group, ok := requeststate.GroupFromContext(ctx); ok && routing.IsGroupContextValid(group) && group.ID == *id {
			return group
		}
	}
	if !snapshotAvailable {
		return nil
	}
	group, err := read(ctx, *id)
	if err != nil {
		return nil
	}
	return group
}
