package apikey_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// compositeAPIKeyRepoStub retains Key records and update masks for binding
// conversion tests.
type compositeAPIKeyRepoStub struct {
	apikey.APIKeyRepository                             // unused methods require setup
	key                     *apikey.APIKey              // retained lookup Key record
	updated                 *apikey.APIKey              // captured replacement Key record
	updatedFields           []apikey.APIKeyUpdateFields // ordered captured write masks
}

// compositeUserRepoStub returns one shallow user view for group permission
// tests.
type compositeUserRepoStub struct {
	identity.UserRepository // unused identity methods require setup

	user *identity.User // configured payer permission view
}

// TestAPIKeyResolveCompositeModel supplies mixed-case, unknown, missing, and
// invalid prefixes against one saved binding. It checks the selected group,
// retention of later model slashes, and distinct prefix errors.
func TestAPIKeyResolveCompositeModel(t *testing.T) {
	key := &apikey.APIKey{IsComposite: true, CompositeGroups: []apikey.APIKeyCompositeGroup{
		{GroupID: 1, Prefix: "GPT", NormalizedPrefix: "gpt"},
	}}

	binding, model, err := key.ResolveCompositeModel("gPt/vendor/model")
	require.NoError(t, err)
	require.Equal(t, int64(1), binding.GroupID)
	require.Equal(t, "vendor/model", model)

	_, _, err = key.ResolveCompositeModel("vendor/model")
	require.ErrorIs(t, err, apikey.ErrCompositeKeyPrefixNotFound)
	_, _, err = key.ResolveCompositeModel("missing-prefix")
	require.ErrorIs(t, err, apikey.ErrCompositeKeyPrefixRequired)
	_, _, err = key.ResolveCompositeModel("bad prefix/model")
	require.ErrorIs(t, err, apikey.ErrCompositeKeyPrefixInvalid)
}

// TestValidateCompositeGroupInputs supplies empty, oversized, duplicate-prefix,
// and duplicate-group binding lists. It checks that structural validation
// returns the specific error for each independently invalid configuration.
func TestValidateCompositeGroupInputs(t *testing.T) {
	_, err := apikey.KeyValidateCompositeGroupInputs(nil)
	require.ErrorIs(t, err, apikey.ErrCompositeKeyGroupsRequired)

	tooMany := make([]apikey.APIKeyCompositeGroupInput, apikey.MaxCompositeAPIKeyGroups+1)
	for index := range tooMany {
		tooMany[index] = apikey.APIKeyCompositeGroupInput{GroupID: int64(index + 1), Prefix: "p" + string(rune('a'+index))}
	}
	_, err = apikey.KeyValidateCompositeGroupInputs(tooMany)
	require.ErrorIs(t, err, apikey.ErrCompositeKeyTooManyGroups)

	_, err = apikey.KeyValidateCompositeGroupInputs([]apikey.APIKeyCompositeGroupInput{
		{GroupID: 1, Prefix: "GPT"}, {GroupID: 2, Prefix: "gpt"},
	})
	require.ErrorIs(t, err, apikey.ErrCompositeKeyPrefixDuplicate)

	_, err = apikey.KeyValidateCompositeGroupInputs([]apikey.APIKeyCompositeGroupInput{
		{GroupID: 1, Prefix: "GPT"}, {GroupID: 1, Prefix: "Claude"},
	})
	require.True(t, errors.Is(err, apikey.ErrCompositeKeyGroupDuplicate))
}

// TestCompositeAPIKeyAuthSnapshotRoundTrip converts a composite Key containing
// a hydrated image-capable group into a snapshot and back. It checks binding
// selection, protocol and multiplier fields, and an ordinary GroupID remaining
// absent.
func TestCompositeAPIKeyAuthSnapshotRoundTrip(t *testing.T) {
	key := &apikey.APIKey{
		ID: 10, UserID: 20, Key: "sk-composite", Name: "composite", Status: billing.StatusActive,
		IsComposite: true,
		User:        &identity.User{ID: 20, Status: billing.StatusActive, Role: identity.RoleUser, Balance: 100},
		CompositeGroups: []apikey.APIKeyCompositeGroup{
			{
				ID: 30, APIKeyID: 10, GroupID: 40, Prefix: "GPT", NormalizedPrefix: "gpt", SortOrder: 1,
				Group: &routing.Group{
					ID:                   40,
					Name:                 "OpenAI",
					Status:               billing.StatusActive,
					IsExclusive:          true,
					RateMultiplier:       1.25,
					AllowImageGeneration: true,
					RPMLimit:             80,
					AllowedProtocols: []protocol.ProtocolID{
						protocol.ProtocolOpenAIResponses,
						protocol.ProtocolOpenAIChatCompletions,
					},
				},
			},
		},
	}
	service := testkit.NewService(nil, nil, nil, nil, nil, nil, nil)
	// Start the fixture after its dependencies are bound so any enabled
	// authentication resources are available during snapshot and service calls.
	service.Start()

	snapshot := service.KeySnapshotFromAPIKey(context.Background(), key)
	require.NotNil(t, snapshot)
	require.Equal(t, apikey.KeyApiKeyAuthSnapshotVersion, snapshot.Version)
	require.Len(t, snapshot.CompositeGroups, 1)

	restored := service.KeySnapshotToAPIKey(key.Key, snapshot)
	require.True(t, restored.IsComposite)
	require.Nil(t, restored.GroupID)
	require.Len(t, restored.CompositeGroups, 1)
	require.Equal(t, "GPT", restored.CompositeGroups[0].Prefix)
	require.True(t, restored.CompositeGroups[0].Group.Hydrated)
	require.Equal(t, 1.25, restored.CompositeGroups[0].Group.RateMultiplier)
	require.True(t, restored.CompositeGroups[0].Group.AllowImageGeneration)
	require.Equal(t, []protocol.ProtocolID{
		protocol.ProtocolOpenAIResponses,
		protocol.ProtocolOpenAIChatCompletions,
	}, restored.CompositeGroups[0].Group.AllowedProtocols)

	binding, model, err := restored.ResolveCompositeModel("gpt/gpt-5")
	require.NoError(t, err)
	require.Equal(t, int64(40), binding.GroupID)
	require.Equal(t, "gpt-5", model)
}

// TestAPIKeyUpdateConvertsBetweenOrdinaryAndComposite changes an owned ordinary
// Key into two prefixed bindings, then back to one group. It checks full
// binding changes, the selected write masks, and rejection of conversion
// without a target.
func TestAPIKeyUpdateConvertsBetweenOrdinaryAndComposite(t *testing.T) {
	groupOne := &routing.Group{ID: 1, Name: "OpenAI", Status: billing.StatusActive, IsExclusive: true}
	groupTwo := &routing.Group{ID: 2, Name: "Claude", Status: billing.StatusActive, IsExclusive: true}
	user := &identity.User{ID: 20, Status: billing.StatusActive, AllowedGroups: []int64{1, 2}, GroupRestrictionsLoaded: true}
	groupID := groupOne.ID
	repo := &compositeAPIKeyRepoStub{key: &apikey.APIKey{
		ID: 10, UserID: user.ID, Key: "sk-convert", Name: "convert", Status: billing.StatusActive,
		GroupID: &groupID, Group: groupOne, User: user,
	}}
	service := testkit.NewService(
		repo,
		&compositeUserRepoStub{user: user},
		&compositeGroupRepoStub{groups: map[int64]*routing.Group{1: groupOne, 2: groupTwo}},
		nil, nil, nil, nil,
	)
	// Start the fixture after its dependencies are bound so any enabled
	// authentication resources are available during snapshot and service calls.
	service.Start()

	toComposite := true
	inputs := []apikey.APIKeyCompositeGroupInput{{GroupID: 1, Prefix: "GPT"}, {GroupID: 2, Prefix: "Claude"}}
	converted, err := service.Update(context.Background(), 10, user.ID, apikey.UpdateAPIKeyRequest{
		IsComposite: &toComposite, CompositeGroups: &inputs,
	})
	require.NoError(t, err)
	require.True(t, converted.IsComposite)
	require.Nil(t, converted.GroupID)
	require.Len(t, converted.CompositeGroups, 2)
	require.Equal(t, apikey.APIKeyUpdateFields{GroupID: true, CompositeConfiguration: true}, repo.updatedFields[0])

	// Request ordinary mode with no target to exercise the service's
	// missing-target rejection before supplying the replacement group for successful conversion.
	toOrdinary := false
	_, err = service.Update(context.Background(), 10, user.ID, apikey.UpdateAPIKeyRequest{IsComposite: &toOrdinary})
	require.ErrorIs(t, err, apikey.ErrCompositeKeyTargetRequired)
	targetGroupID := int64(2)
	converted, err = service.Update(context.Background(), 10, user.ID, apikey.UpdateAPIKeyRequest{
		IsComposite: &toOrdinary, GroupID: &targetGroupID,
	})
	require.NoError(t, err)
	require.False(t, converted.IsComposite)
	require.Equal(t, targetGroupID, *converted.GroupID)
	require.Empty(t, converted.CompositeGroups)
	require.Equal(t, apikey.APIKeyUpdateFields{GroupID: true, CompositeConfiguration: true}, repo.updatedFields[1])
}

// TestCompositeAPIKeyUpdateAddsMappingsWithoutConfirmation supplies two group
// mappings while converting an owned Key to composite mode. It checks that the
// service accepts both allowed groups and selects the group and composite
// configuration columns.
func TestCompositeAPIKeyUpdateAddsMappingsWithoutConfirmation(t *testing.T) {
	groupOne := &routing.Group{ID: 1, Name: "One", Status: billing.StatusActive, IsExclusive: true}
	groupTwo := &routing.Group{ID: 2, Name: "Two", Status: billing.StatusActive, IsExclusive: true}
	user := &identity.User{ID: 20, Status: billing.StatusActive, AllowedGroups: []int64{1, 2}, GroupRestrictionsLoaded: true}
	repo := &compositeAPIKeyRepoStub{key: &apikey.APIKey{
		ID: 10, UserID: user.ID, Key: "sk-composite", Name: "composite", Status: billing.StatusActive, User: user,
	}}
	service := testkit.NewService(
		repo,
		&compositeUserRepoStub{user: user},
		&compositeGroupRepoStub{groups: map[int64]*routing.Group{1: groupOne, 2: groupTwo}},
		nil, nil, nil, nil,
	)
	// Start the fixture after its dependencies are bound so any enabled
	// authentication resources are available during snapshot and service calls.
	service.Start()
	toComposite := true
	inputs := []apikey.APIKeyCompositeGroupInput{{GroupID: 1, Prefix: "One"}, {GroupID: 2, Prefix: "Two"}}
	updated, err := service.Update(context.Background(), 10, user.ID, apikey.UpdateAPIKeyRequest{
		IsComposite: &toComposite, CompositeGroups: &inputs,
	})
	require.NoError(t, err)
	require.Len(t, updated.CompositeGroups, 2)
	require.Equal(t, apikey.APIKeyUpdateFields{GroupID: true, CompositeConfiguration: true}, repo.updatedFields[0])
}

// GetByID returns a shallow Key copy with an independently allocated nonempty
// binding slice. Group and override pointers stay shared, and this fixture
// ignores the requested ID and context.
func (s *compositeAPIKeyRepoStub) GetByID(_ context.Context, _ int64) (*apikey.APIKey, error) {
	copyKey := *s.key
	copyKey.CompositeGroups = apikey.KeyCloneCompositeBindings(s.key.CompositeGroups)
	return &copyKey, nil
}

// Update captures a shallow Key copy and a copied nonempty binding slice, then
// records its write mask. The captured result becomes the next lookup record;
// nested group and override pointers remain shared.
func (s *compositeAPIKeyRepoStub) Update(_ context.Context, key *apikey.APIKey, fields apikey.APIKeyUpdateFields) error {
	copyKey := *key
	copyKey.CompositeGroups = apikey.KeyCloneCompositeBindings(key.CompositeGroups)
	s.updated = &copyKey
	s.key = &copyKey
	s.updatedFields = append(s.updatedFields, fields)
	return nil
}

// GetByID returns a shallow copy of the configured user, ignoring the lookup
// ID. Tests supply a nonnil user; its slices, maps and pointers remain shared
// with the fixture.
func (s *compositeUserRepoStub) GetByID(_ context.Context, _ int64) (*identity.User, error) {
	copyUser := *s.user
	return &copyUser, nil
}
