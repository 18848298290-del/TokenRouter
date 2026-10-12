package apikey

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// runtimeGroupRepository returns one configured target group for fallback
// permission tests.
type runtimeGroupRepository struct {
	routing.GroupRepository                // unused methods require setup
	group                   *routing.Group // configured fallback target
}

// GetByIDLite returns the configured target pointer for every requested group
// ID. The fixture ignores the context and ID, and callers treat its shared
// group as input during each scenario.
func (r runtimeGroupRepository) GetByIDLite(context.Context, int64) (*routing.Group, error) {
	return r.group, nil
}

// TestRuntimeGroupFallbackPermissionBoundaries exercises public, exclusive,
// composite, and team targets with distinct permission states. It checks
// accepted or rejected resolution, RPM-override replacement, and retention of
// the previous binding and override.
func TestRuntimeGroupFallbackPermissionBoundaries(t *testing.T) {
	for _, scenario := range []struct {
		name string // permission scenario name
		// private selects an exclusive target, allowed grants that target, and
		// disabledPublic denies its public form. composite enables binding checks;
		// bound includes the target, while team requires a resolvable owner.
		// private: exclusive target flag; allowed: exclusive target grant;
		// disabledPublic: public target denial; composite: composite Key mode;
		// bound: target binding present; team: team payer requirement.
		private, allowed, disabledPublic, composite, bound, team bool
		want                                                     bool // expected resolution success
	}{
		{name: "public", want: true},
		{name: "disabled public", disabledPublic: true},
		{name: "exclusive denied", private: true},
		{name: "exclusive allowed", private: true, allowed: true, want: true},
		{name: "composite outside bindings", composite: true},
		{name: "composite inside bindings", composite: true, bound: true, want: true},
		{name: "team owner unavailable", team: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			targetID := int64(2)
			oldRPM := 3
			original := &APIKey{
				ID: 1, UserID: 1, GroupID: new(int64(1)), FallbackWhenGroupUnavailable: true, IsComposite: scenario.composite,
				Group: &routing.Group{ID: 1, Status: routing.StatusDisabled, UnavailableFallbackGroupID: &targetID},
				User:  &User{ID: 1, Status: StatusActive, UserGroupRPMOverride: &oldRPM},
			}
			if scenario.allowed {
				original.User.AllowedGroups = []int64{2}
			}
			if scenario.disabledPublic {
				original.User.DisabledPublicGroups = []int64{2}
			}
			if scenario.composite {
				original.CompositeGroups = []APIKeyCompositeGroup{{GroupID: 1}}
			}
			if scenario.bound {
				original.CompositeGroups = append(original.CompositeGroups, APIKeyCompositeGroup{GroupID: 2})
			}
			if scenario.team {
				original.TeamID = new(int64(3))
			}
			service := &APIKeyService{groupRepo: runtimeGroupRepository{group: &routing.Group{ID: 2, Status: routing.StatusActive, IsExclusive: scenario.private}}}
			resolved, err := service.ResolveRuntimeGroup(context.Background(), original, targetID)
			if scenario.want {
				require.NoError(t, err)
				require.Equal(t, targetID, *resolved.GroupID)
				require.Nil(t, resolved.User.UserGroupRPMOverride)
			} else {
				require.Error(t, err)
				require.Nil(t, resolved)
			}
			fallback := service.KeyApplyExplicitGroupFallback(context.Background(), original)
			if scenario.want {
				require.Equal(t, targetID, *fallback.GroupID)
			} else {
				require.Same(t, original, fallback)
			}
			require.Equal(t, int64(1), *original.GroupID)
			require.Equal(t, oldRPM, *original.User.UserGroupRPMOverride)
		})
	}
}
