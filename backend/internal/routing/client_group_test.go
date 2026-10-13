package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// Client-restricted group resolution depends on read order and policy-specific
// stopping conditions. These fixtures isolate cyclic targets, unrestricted
// groups, invalid fallback identifiers, and missing snapshots so callers can
// retain the intended error precedence and client-detection count.

// TestGatewayService_ResolveGatewayGroup_DetectsFallbackCycle links two
// restricted groups back to each other. Resolution must return a cycle error
// with no selected group or identifier after a repeated target is reached.
func TestGatewayService_ResolveGatewayGroup_DetectsFallbackCycle(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10)
	fallbackID := int64(11)

	group := &Group{
		ID: groupID,

		Status:          StatusActive,
		ClaudeCodeOnly:  true,
		FallbackGroupID: &fallbackID,
	}
	fallbackGroup := &Group{
		ID: fallbackID,

		Status:          StatusActive,
		ClaudeCodeOnly:  true,
		FallbackGroupID: &groupID,
	}

	groups := map[int64]*Group{groupID: group, fallbackID: fallbackGroup}
	gotGroup, gotID, err := ResolveClientGroup(ctx, &groupID, func(_ context.Context, id int64) (*Group, error) { return groups[id], nil }, func(context.Context) bool { return false }, ClientGroupPolicy{})
	require.Error(t, err)
	require.Nil(t, gotGroup)
	require.Nil(t, gotID)
	require.Contains(t, err.Error(), "fallback group cycle")
}

// TestClientGroupResolutionKeepsReadOrder falls from a restricted group into an
// unrestricted group. Recorded calls must show the first read, one client
// check, and the next read with no second check.
func TestClientGroupResolutionKeepsReadOrder(t *testing.T) {
	first, next := int64(1), int64(2)
	var sequence []string
	reader := func(_ context.Context, id int64) (*Group, error) {
		if id == first {
			sequence = append(sequence, "first")
			return &Group{ID: id, ClaudeCodeOnly: true, FallbackGroupID: &next}, nil
		}
		sequence = append(sequence, "next")
		return &Group{ID: id}, nil
	}
	group, id, err := ResolveClientGroup(context.Background(), &first, reader, func(context.Context) bool {
		sequence = append(sequence, "client")
		return false
	}, ClientGroupPolicy{})
	require.NoError(t, err)
	require.Equal(t, next, *id)
	require.Equal(t, next, group.ID)
	require.Equal(t, []string{"first", "client", "next"}, sequence)
}

// TestClientGroupResolutionKeepsFallbackIDPolicies supplies a zero fallback
// target under both policies. Rejecting it must return ErrClaudeCodeOnly before
// another read, while permissive resolution exposes the reader's missing-group
// error.
func TestClientGroupResolutionKeepsFallbackIDPolicies(t *testing.T) {
	for _, reject := range []bool{false, true} {
		id, invalid := int64(1), int64(0)
		missing := errors.New("fixture group not found")
		var calls []int64
		_, _, err := ResolveClientGroup(context.Background(), &id, func(_ context.Context, id int64) (*Group, error) {
			calls = append(calls, id)
			if id == invalid {
				return nil, missing
			}
			return &Group{ID: id, ClaudeCodeOnly: true, FallbackGroupID: &invalid}, nil
		}, func(context.Context) bool { return false }, ClientGroupPolicy{RejectNonPositiveFallback: reject})
		if reject {
			require.ErrorIs(t, err, ErrClaudeCodeOnly)
			require.Equal(t, []int64{1}, calls)
		} else {
			require.ErrorIs(t, err, missing)
			require.Equal(t, []int64{1, 0}, calls)
		}
	}
}

// TestClientGroupResolutionKeepsMissingSnapshot returns nil from the reader
// with retention enabled. Resolution must preserve the supplied ID and skip the
// client callback while returning no group and no error.
func TestClientGroupResolutionKeepsMissingSnapshot(t *testing.T) {
	id := int64(9)
	group, resolved, err := ResolveClientGroup(context.Background(), &id, func(context.Context, int64) (*Group, error) {
		return nil, nil
	}, func(context.Context) bool {
		t.Fatal("缺失快照不得读取客户端标志")
		return false
	}, ClientGroupPolicy{KeepMissingSnapshot: true})
	require.NoError(t, err)
	require.Nil(t, group)
	require.Equal(t, id, *resolved)
}
