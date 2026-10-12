package selection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type authorizedGroupReader struct {
	groups  map[int64]*routing.Group
	readIDs []int64
}

func (r *authorizedGroupReader) GetByID(_ context.Context, id int64) (*routing.Group, error) {
	r.readIDs = append(r.readIDs, id)
	return r.groups[id], nil
}

func (r *authorizedGroupReader) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	return r.GetByID(ctx, id)
}

// TestSelectorsNeverFollowClientFallbackWithoutAdmission checks that selectors reject fallback groups lacking entry admission.
func TestSelectorsNeverFollowClientFallbackWithoutAdmission(t *testing.T) {
	sourceID, targetID := int64(91), int64(92)
	source := &routing.Group{ID: sourceID, Hydrated: true, Status: routing.StatusActive, ClaudeCodeOnly: true, FallbackGroupID: &targetID}
	for _, name := range []string{"generic", "compatible", "gemini"} {
		t.Run(name, func(t *testing.T) {
			target := &routing.Group{ID: targetID, Hydrated: true, Status: routing.StatusActive, SchedulerType: routing.GroupSchedulerTypeAdvanced}
			groups := &authorizedGroupReader{groups: map[int64]*routing.Group{sourceID: source, targetID: target}}
			repo := &mixedGroupProviders{values: []gatewayprovider.ExecutionProvider{mixedGroupProvider(9, capability.PlatformGemini, "shared", targetID)}}
			reads := Reads{Providers: repo, Groups: groups}
			var choose func(context.Context, *int64, string, string) (*gatewayprovider.ExecutionProvider, error)
			switch name {
			case "generic":
				choose = NewGeneric(GenericDependencies{Reads: reads}, DefaultOptions()).SelectProviderForModel
			case "compatible":
				choose = NewCompatible(CompatibleDependencies{Reads: reads}, DefaultOptions()).SelectProviderForModel
			case "gemini":
				selector := NewGemini(GeminiDependencies{Reads: reads}, DefaultOptions())
				choose = func(ctx context.Context, groupID *int64, sessionHash, model string) (*gatewayprovider.ExecutionProvider, error) {
					return selector.SelectProviderForModelWithExclusions(ctx, groupID, sessionHash, model, nil)
				}
			}
			ctx := requeststate.WithGroup(context.Background(), source)
			ctx = requeststate.WithRoutePlan(ctx, routing.Plan(routing.PlanInput{Group: source}))
			_, err := choose(ctx, &sourceID, "", "shared")
			require.ErrorIs(t, err, routing.ErrClaudeCodeOnly)
			require.Empty(t, repo.groupQueries)
			require.NotContains(t, groups.readIDs, targetID)
			_, err = choose(ctx, &targetID, "", "shared")
			require.ErrorIs(t, err, apikey.ErrGroupNotAllowed)
			require.Empty(t, repo.groupQueries)
			for _, mode := range []routing.GroupSchedulerType{routing.GroupSchedulerTypeBasic, routing.GroupSchedulerTypeAdvanced} {
				target.SchedulerType = mode
				admitted := requeststate.WithGroup(context.Background(), target)
				admitted = requeststate.WithRoutePlan(admitted, routing.Plan(routing.PlanInput{Group: target}))
				selected, err := choose(admitted, &targetID, "", "shared")
				require.NoError(t, err)
				require.Equal(t, int64(9), selected.Record.ID)
			}
		})
	}
}

func TestCurrentSelectionGroupRejectsPlanMismatchWithoutGroupSnapshot(t *testing.T) {
	authorized, other := int64(91), int64(92)
	ctx := requeststate.WithRoutePlan(context.Background(), routing.Plan(routing.PlanInput{GroupID: &authorized}))
	_, err := currentSelectionGroup(ctx, &other, nil)
	require.ErrorIs(t, err, apikey.ErrGroupNotAllowed)
}

// TestCurrentSelectionGroupRejectsUnhydratedGroupMismatch checks that even partial group snapshots constrain the authorized ID.
func TestCurrentSelectionGroupRejectsUnhydratedGroupMismatch(t *testing.T) {
	authorized, other := int64(91), int64(92)
	ctx := requeststate.WithGroup(context.Background(), &routing.Group{ID: authorized})
	_, err := currentSelectionGroup(ctx, &other, func(context.Context, int64) (*routing.Group, error) {
		t.Fatal("不应读取未经授权的目标分组")
		return nil, nil
	})
	require.ErrorIs(t, err, apikey.ErrGroupNotAllowed)
}
