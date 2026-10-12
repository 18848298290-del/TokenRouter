package selection

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// currentSelectionGroup validates the admitted group. The entry handler must authorize a fallback group before selection.
func currentSelectionGroup(ctx context.Context, id *int64, read func(context.Context, int64) (*routing.Group, error)) (*routing.Group, error) {
	if id == nil || *id <= 0 {
		return nil, nil
	}
	if plan, ok := requeststate.RoutePlanFromContext(ctx); ok && plan.GroupID() != *id {
		return nil, apikey.ErrGroupNotAllowed
	}
	group, ok := requeststate.GroupFromContext(ctx)
	if ok && group != nil && group.ID > 0 && group.ID != *id {
		return nil, apikey.ErrGroupNotAllowed
	}
	if ok && routing.IsGroupContextValid(group) {
		return validateCurrentSelectionGroup(ctx, group, *id)
	} else if read != nil {
		var err error
		group, err = read(ctx, *id)
		if err != nil {
			return nil, err
		}
		if group == nil {
			return nil, routing.ErrGroupNotFound
		}
	} else {
		group = nil
	}
	return validateCurrentSelectionGroup(ctx, group, *id)
}

// validateCurrentSelectionGroup applies the same group identity and client checks to cached and fetched groups.
func validateCurrentSelectionGroup(ctx context.Context, group *routing.Group, id int64) (*routing.Group, error) {
	if group != nil {
		if group.ID != id {
			return nil, apikey.ErrGroupNotAllowed
		}
		if group.ClaudeCodeOnly && !requeststate.IsClaudeCodeClient(ctx) {
			return nil, routing.ErrClaudeCodeOnly
		}
	}
	return group, nil
}
