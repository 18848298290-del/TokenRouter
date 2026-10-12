package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGatewayService_ResolveGatewayGroup_DetectsFallbackCycle checks rejection of cyclic group fallbacks.
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

// TestClientGroupResolutionKeepsReadOrder checks group reads before client checks and short-circuiting for
// unrestricted groups.
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

// TestClientGroupResolutionKeepsFallbackIDPolicies checks error precedence for invalid fallback IDs.
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

// TestClientGroupResolutionKeepsMissingSnapshot checks retained IDs and call counts when a snapshot is missing.
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
