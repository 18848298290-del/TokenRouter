package selection

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// A selector receives a group that entry admission has already authorized. A
// matching route plan and request group constrain the requested ID before any
// repository read, including when the request group is only partially hydrated.
// A caller wanting a fallback group first performs admission and supplies that
// group's request state.
//
// Valid request groups can satisfy selection directly; incomplete groups use
// the supplied reader when available. Fetched records receive the same identity
// and Claude Code client checks as cached records. Missing IDs bypass group
// reads, while conflicting positive IDs fail before another group's providers
// can be queried.

// currentSelectionGroup validates the admitted group before selection reads its
// providers. A nil or nonpositive ID returns no group, while a conflicting
// request plan or group snapshot returns ErrGroupNotAllowed before invoking
// read.
//
// A valid request group is reused; otherwise the supplied reader may fetch the
// requested ID. Reader errors propagate, missing fetched groups return
// ErrGroupNotFound, and both sources receive the same identity and Claude Code
// client check. Entry admission owns authorization of any fallback group.
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
		// A partial request snapshot cannot supply selection settings. Fetch the
		// admitted ID after mismatch checks so stale context cannot redirect this read.
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

// validateCurrentSelectionGroup checks group identity and the Claude Code
// client requirement for either source. Nil groups pass through; mismatched IDs
// return ErrGroupNotAllowed and a restricted non-Claude client receives
// ErrClaudeCodeOnly.
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
