package selection

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	schedulercore "github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// parameters.go resolves request-group overrides for effective advanced
// scheduler settings.
// Advanced scheduler overrides apply to the request's final group after
// admission. Valid group context takes precedence over repository lookup, and a
// missing group supplies empty overrides so Parameters can resolve global
// process and runtime settings.

// advancedSchedulerEffectiveSettingsForGroup delegates group overrides to the
// selector's scheduler parameters. A nil selector uses Parameters defaults, and
// basic or missing groups contribute empty overrides so their retained advanced
// settings do not apply.
func (s *Compatible) advancedSchedulerEffectiveSettingsForGroup(ctx context.Context, group *routing.Group) policy.EffectiveSettings {
	var parameters *schedulercore.Parameters
	if s != nil {
		parameters = s.schedulerParameters
	}
	return parameters.Effective(ctx, schedulerGroupOverrides(group))
}

// advancedSchedulerEffectiveSettingsForRequest resolves the request's final
// group before applying advanced overrides. A nil context becomes Background,
// and an unavailable group leaves Parameters to use global settings for this
// call.
func (s *Compatible) advancedSchedulerEffectiveSettingsForRequest(
	ctx context.Context,
	groupID *int64,
) policy.EffectiveSettings {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.advancedSchedulerEffectiveSettingsForGroup(ctx, s.advancedSchedulerGroupForRequest(ctx, groupID))
}

// advancedSchedulerGroupForRequest reuses a matching valid group or permits a
// scheduling-group read when snapshots are configured. Missing request IDs,
// unavailable snapshot reads, and read failures produce nil for global-setting
// resolution.
func (s *Compatible) advancedSchedulerGroupForRequest(ctx context.Context, id *int64) *routing.Group {
	return schedulerRequestGroup(ctx, id, s != nil && s.schedulerSnapshot != nil, s.readSchedulingGroup)
}

// schedulerGroupOverrides returns the override value attached to an advanced
// group. Basic and missing groups return empty overrides, leaving every
// effective field to inherit the process and runtime settings.
func schedulerGroupOverrides(group *routing.Group) policy.GroupAdvancedSchedulerOverrides {
	if group != nil && group.UsesAdvancedScheduler() {
		return group.AdvancedSchedulerOverrides
	}
	return policy.GroupAdvancedSchedulerOverrides{}
}

// schedulerRequestGroup prefers a valid context group whose ID matches the
// requested positive ID. Otherwise it invokes read when snapshotAvailable is
// true; absent eligibility or a read error returns nil. Callers supply a
// callable reader when resolution can reach that repository branch.
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
	// The scheduling snapshot binding enables this full group read for overrides.
	// Failed reads yield no group, allowing Parameters to resolve its global settings.
	group, err := read(ctx, *id)
	if err != nil {
		return nil
	}
	return group
}
