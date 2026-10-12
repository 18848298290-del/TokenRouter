package routing

import (
	"context"
	"errors"
	"fmt"
)

// ErrClaudeCodeOnly indicates that a group accepts only Claude Code clients.
var ErrClaudeCodeOnly = errors.New("this group only allows Claude Code clients")

// ClientGroupPolicy controls missing-snapshot handling and nonpositive fallback IDs.
type ClientGroupPolicy struct {
	KeepMissingSnapshot       bool
	RejectNonPositiveFallback bool
}

// ResolveClientGroup follows client-restriction fallbacks, reading each group before checking the client.
// Unless KeepMissingSnapshot is set, read must return a non-nil group or an error.
// @project-doc docs/domains/gateway_policy_controls.md#gateway_policy_layers
func ResolveClientGroup(ctx context.Context, groupID *int64, read func(context.Context, int64) (*Group, error), isClient func(context.Context) bool, policy ClientGroupPolicy) (*Group, *int64, error) {
	if groupID == nil {
		return nil, nil, nil
	}
	currentID := *groupID
	visited := map[int64]struct{}{}
	for {
		if _, seen := visited[currentID]; seen {
			return nil, nil, fmt.Errorf("fallback group cycle detected")
		}
		visited[currentID] = struct{}{}
		group, err := read(ctx, currentID)
		if err != nil {
			return nil, nil, err
		}
		if group == nil && policy.KeepMissingSnapshot {
			return nil, &currentID, nil
		}
		if !group.ClaudeCodeOnly || isClient(ctx) {
			return group, &currentID, nil
		}
		if group.FallbackGroupID == nil || (policy.RejectNonPositiveFallback && *group.FallbackGroupID <= 0) {
			return nil, nil, ErrClaudeCodeOnly
		}
		currentID = *group.FallbackGroupID
	}
}
