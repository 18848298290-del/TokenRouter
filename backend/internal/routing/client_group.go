package routing

import (
	"context"
	"errors"
	"fmt"
)

// client_group.go follows client-restriction fallback groups and detects
// cycles before provider selection.
// A Claude Code restriction can move a request through configured fallback
// groups before provider selection. Reading each group first keeps client
// detection tied to its current policy, and unrestricted groups stop the chain
// before client detection runs.
//
// Visited identifiers stop fallback cycles even when each group is individually
// readable. Callers choose whether a missing snapshot may retain its ID and
// whether a nonpositive fallback is rejected before a read. The result
// identifies the selected group; gateway entries still authorize that target's
// permissions and funding before routing it.

// ErrClaudeCodeOnly identifies a client-restricted group whose configured
// fallback cannot be followed under the selected resolution policy.
var ErrClaudeCodeOnly = errors.New("this group only allows Claude Code clients")

// ClientGroupPolicy configures missing snapshot and invalid fallback identifier
// handling.
type ClientGroupPolicy struct {
	// KeepMissingSnapshot accepts a nil group returned with no error and retains
	// its ID. When false, the reader is responsible for returning a group or
	// error.
	KeepMissingSnapshot bool
	// RejectNonPositiveFallback stops the chain with ErrClaudeCodeOnly for a
	// nonpositive configured target. Otherwise the target is passed to read and
	// its error wins.
	RejectNonPositiveFallback bool
}

// ResolveClientGroup reads and follows Claude Code fallback groups until the
// client is accepted or a failure stops resolution. Nil groupID returns no
// group, ID, or error; reader errors pass through before client checks.
//
// KeepMissingSnapshot permits a nil group with no reader error to return its
// unresolved ID. Otherwise read must return a nonnil group. Visited IDs detect
// cycles before rereading, and RejectNonPositiveFallback decides whether an
// invalid target returns ErrClaudeCodeOnly or reaches the reader. Callers
// authorize the returned target before provider selection.
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
		// Client rejection chooses the next group; rereading on the next iteration
		// applies that group's own restriction and detects repeated IDs.
		currentID = *group.FallbackGroupID
	}
}
