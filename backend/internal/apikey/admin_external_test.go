package apikey_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// userRepoStubForGroupUpdate implements UserRepository for AdminUpdateAPIKeyGroupID tests.
type userRepoStubForGroupUpdate struct {
	addGroupErr    error
	addGroupCalled bool
	addedUserID    int64
	addedGroupID   int64
	user           *identity.User
	getErr         error
}

// apiKeyRepoStubForGroupUpdate implements APIKeyRepository for AdminUpdateAPIKeyGroupID tests.
type apiKeyRepoStubForGroupUpdate struct {
	key       *apikey.APIKey
	getErr    error
	updateErr error
	updated   *apikey.APIKey // captures what was passed to Update
}

// groupRepoStubForGroupUpdate implements GroupRepository for AdminUpdateAPIKeyGroupID tests.
type groupRepoStubForGroupUpdate struct {
	group          *routing.Group
	getErr         error
	lastGetByIDArg int64
}

// keyGroupsFixture returns group copies from source for the methods used by these tests.
type keyGroupsFixture struct {
	apikey.GroupRepository
	source *groupRepoStubForGroupUpdate
}

// authCacheInvalidatorStub records credentials invalidated by admin operations.
type authCacheInvalidatorStub struct{ keys []string }

func TestAdminService_AdminUpdateAPIKeyGroupID_KeyNotFound(t *testing.T) {
	repo := &apiKeyRepoStubForGroupUpdate{getErr: apikey.ErrAPIKeyNotFound}
	svc := newKeyAdminForTest(repo, nil, nil, nil)

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 999, int64Ptr(1))
	require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_NilGroupID_NoOp(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, Key: "sk-test", GroupID: int64Ptr(5)}
	repo := &apiKeyRepoStubForGroupUpdate{key: existing}
	svc := newKeyAdminForTest(repo, nil, nil, nil)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.APIKey.ID)
	// An omitted group ID leaves the key untouched.
	require.Nil(t, repo.updated)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_Unbind(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, Key: "sk-test", GroupID: int64Ptr(5), Group: &routing.Group{ID: 5, Name: "Old"}}
	repo := &apiKeyRepoStubForGroupUpdate{key: existing}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(repo, nil, nil, cache)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(0))
	require.NoError(t, err)
	require.Nil(t, got.APIKey.GroupID, "group_id should be nil after unbind")
	require.Nil(t, got.APIKey.Group, "group object should be nil after unbind")
	require.NotNil(t, repo.updated, "Update should have been called")
	require.Nil(t, repo.updated.GroupID)
	require.Equal(t, []string{"sk-test"}, cache.keys, "cache should be invalidated")
}

func TestAdminService_AdminUpdateAPIKeyGroupID_BindActiveGroup(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Pro", Status: billing.StatusActive}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42}}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, cache)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	require.Equal(t, int64(10), *got.APIKey.GroupID)
	require.Equal(t, int64(10), *apiKeyRepo.updated.GroupID)
	require.Equal(t, []string{"sk-test"}, cache.keys)
	// The repository lookup uses the requested group ID.
	require.Equal(t, int64(10), groupRepo.lastGetByIDArg)
	// The response includes the selected group details.
	require.NotNil(t, got.APIKey.Group)
	require.Equal(t, "Pro", got.APIKey.Group.Name)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_SameGroup_Idempotent(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: int64Ptr(10), Group: &routing.Group{ID: 10, Name: "Pro"}}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Pro", Status: billing.StatusActive}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42}}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, cache)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	require.Equal(t, int64(10), *got.APIKey.GroupID)
	// Selecting the current group still writes the key and invalidates its cache.
	require.NotNil(t, apiKeyRepo.updated)
	require.Equal(t, []string{"sk-test"}, cache.keys)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_GroupNotFound(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, Key: "sk-test"}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{getErr: routing.ErrGroupNotFound}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, nil, nil)

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(99))
	require.ErrorIs(t, err, routing.ErrGroupNotFound)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_GroupNotActive(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, Key: "sk-test"}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 5, Status: billing.StatusDisabled}}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, nil, nil)

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(5))
	require.Error(t, err)
	require.Equal(t, "GROUP_NOT_ACTIVE", apperror.Reason(err))
}

func TestAdminService_AdminUpdateAPIKeyGroupID_UpdateFails(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, Key: "sk-test", GroupID: int64Ptr(3)}
	repo := &apiKeyRepoStubForGroupUpdate{key: existing, updateErr: errors.New("db write error")}
	svc := newKeyAdminForTest(repo, nil, nil, nil)

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(0))
	require.Error(t, err)
	require.Contains(t, err.Error(), "update api key")
}

func TestAdminService_AdminUpdateAPIKeyGroupID_NegativeGroupID(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, Key: "sk-test"}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	svc := newKeyAdminForTest(apiKeyRepo, nil, nil, nil)

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(-5))
	require.Error(t, err)
	require.Equal(t, "INVALID_GROUP_ID", apperror.Reason(err))
}

func TestAdminService_AdminUpdateAPIKeyGroupID_PointerIsolation(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Pro", Status: billing.StatusActive}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42}}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, cache)

	inputGID := int64(10)
	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, &inputGID)
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	// The stored group ID must be independent of the input pointer.
	inputGID = 999
	require.Equal(t, int64(10), *got.APIKey.GroupID)
	require.Equal(t, int64(10), *apiKeyRepo.updated.GroupID)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_NilCacheInvalidator(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test"}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 7, Status: billing.StatusActive}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42}}
	// Cache invalidation is optional.
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, nil)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(7))
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	require.Equal(t, int64(7), *got.APIKey.GroupID)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_ExclusiveGroup_AddsAllowedGroup(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Exclusive", Status: billing.StatusActive, IsExclusive: true}}
	userRepo := &userRepoStubForGroupUpdate{}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, cache)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	require.Equal(t, int64(10), *got.APIKey.GroupID)
	// Exclusive group binding grants access to the key owner.
	require.True(t, userRepo.addGroupCalled)
	require.Equal(t, int64(42), userRepo.addedUserID)
	require.Equal(t, int64(10), userRepo.addedGroupID)
	// The result reports the granted group for the API response.
	require.True(t, got.AutoGrantedGroupAccess)
	require.NotNil(t, got.GrantedGroupID)
	require.Equal(t, int64(10), *got.GrantedGroupID)
	require.Equal(t, "Exclusive", got.GrantedGroupName)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_NonExclusiveGroup_NoAllowedGroupUpdate(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Public", Status: billing.StatusActive, IsExclusive: false}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42}}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, cache)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	// Public groups do not require an explicit access grant.
	require.False(t, userRepo.addGroupCalled)
	require.False(t, got.AutoGrantedGroupAccess)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_PublicGroupDisabledForUser_ReturnsError(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Public", Status: billing.StatusActive, IsExclusive: false}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42, DisabledPublicGroups: []int64{10}}}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, cache)

	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.ErrorIs(t, err, apikey.ErrGroupNotAllowed)
	require.Nil(t, apiKeyRepo.updated)
	require.Empty(t, cache.keys)
	require.False(t, userRepo.addGroupCalled)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_DoesNotRequireSubscriptionForBinding(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Sub", Status: billing.StatusActive, IsExclusive: false}}
	userRepo := &userRepoStubForGroupUpdate{user: &identity.User{ID: 42}}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, nil)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.NoError(t, err)
	require.NotNil(t, got.APIKey.GroupID)
	require.Equal(t, int64(10), *got.APIKey.GroupID)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_ExclusiveGroup_AllowedGroupAddFails_ReturnsError(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: nil}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	groupRepo := &groupRepoStubForGroupUpdate{group: &routing.Group{ID: 10, Name: "Exclusive", Status: billing.StatusActive, IsExclusive: true}}
	userRepo := &userRepoStubForGroupUpdate{addGroupErr: errors.New("db error")}
	svc := newKeyAdminForTest(apiKeyRepo, groupRepo, userRepo, nil)

	// A failed access grant aborts the key update.
	_, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(10))
	require.Error(t, err)
	require.Contains(t, err.Error(), "add group to user allowed groups")
	require.True(t, userRepo.addGroupCalled)
	// The failed grant leaves the key untouched.
	require.Nil(t, apiKeyRepo.updated)
}

func TestAdminService_AdminUpdateAPIKeyGroupID_Unbind_NoAllowedGroupUpdate(t *testing.T) {
	existing := &apikey.APIKey{ID: 1, UserID: 42, Key: "sk-test", GroupID: int64Ptr(10), Group: &routing.Group{ID: 10, Name: "Exclusive"}}
	apiKeyRepo := &apiKeyRepoStubForGroupUpdate{key: existing}
	userRepo := &userRepoStubForGroupUpdate{}
	cache := &authCacheInvalidatorStub{}
	svc := newKeyAdminForTest(apiKeyRepo, nil, userRepo, cache)

	got, err := svc.AdminUpdateAPIKeyGroupID(context.Background(), 1, int64Ptr(0))
	require.NoError(t, err)
	require.Nil(t, got.APIKey.GroupID)
	// Unbinding a key leaves the user's group permissions unchanged.
	require.False(t, userRepo.addGroupCalled)
	require.False(t, got.AutoGrantedGroupAccess)
}

func (s *userRepoStubForGroupUpdate) AddGroupToAllowedGroups(_ context.Context, userID int64, groupID int64) error {
	s.addGroupCalled = true
	s.addedUserID = userID
	s.addedGroupID = groupID
	return s.addGroupErr
}

func (s *userRepoStubForGroupUpdate) Create(context.Context, *identity.User) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) CreateWithNormalizedEmailGuard(context.Context, *identity.User, string) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) GetByID(context.Context, int64) (*identity.User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.user == nil {
		return nil, identity.ErrUserNotFound
	}
	clone := *s.user
	if s.user.AllowedGroups != nil {
		clone.AllowedGroups = append([]int64(nil), s.user.AllowedGroups...)
	}
	if s.user.DisabledPublicGroups != nil {
		clone.DisabledPublicGroups = append([]int64(nil), s.user.DisabledPublicGroups...)
	}
	return &clone, nil
}

func (s *userRepoStubForGroupUpdate) GetByEmail(context.Context, string) (*identity.User, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) GetFirstAdmin(context.Context) (*identity.User, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) Update(context.Context, *identity.User, identity.UserUpdateFields) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) UpdateWithNormalizedEmailGuard(context.Context, *identity.User, string, identity.UserUpdateFields) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) Delete(context.Context, int64) error { panic("unexpected") }

func (s *userRepoStubForGroupUpdate) GetUserAvatar(context.Context, int64) (*identity.UserAvatar, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) UpsertUserAvatar(context.Context, int64, identity.UpsertUserAvatarInput) (*identity.UserAvatar, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) DeleteUserAvatar(context.Context, int64) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) List(context.Context, pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) ListWithFilters(context.Context, pagination.PaginationParams, identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) UpdateBalance(context.Context, int64, float64) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) AddBalance(context.Context, int64, float64) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) DeductBalance(context.Context, int64, float64) (float64, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

func (s *userRepoStubForGroupUpdate) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

func (s *userRepoStubForGroupUpdate) UpdateConcurrency(context.Context, int64, int) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	return 0, nil
}

func (s *userRepoStubForGroupUpdate) ExistsByEmail(context.Context, string) (bool, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) ExistsByNormalizedEmail(context.Context, string) (bool, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) LockRegistrationEmail(context.Context, string) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) UpdateTotpSecret(context.Context, int64, *string) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) EnableTotp(context.Context, int64) error { panic("unexpected") }

func (s *userRepoStubForGroupUpdate) DisableTotp(context.Context, int64) error { panic("unexpected") }

func (s *userRepoStubForGroupUpdate) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	panic("unexpected GetByIDIncludeDeleted call")
}

func (s *userRepoStubForGroupUpdate) ListUserAuthIdentities(context.Context, int64) ([]identity.UserAuthIdentityRecord, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) UnbindUserAuthProvider(context.Context, int64, string) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	panic("unexpected")
}

func (s *userRepoStubForGroupUpdate) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) GetByID(_ context.Context, _ int64) (*apikey.APIKey, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	clone := *s.key
	return &clone, nil
}

func (s *apiKeyRepoStubForGroupUpdate) RotateCredential(context.Context, *apikey.APIKey, string) error {
	panic("unexpected RotateCredential call")
}

func (s *apiKeyRepoStubForGroupUpdate) Update(_ context.Context, key *apikey.APIKey, _ apikey.APIKeyUpdateFields) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	clone := *key
	s.updated = &clone
	return nil
}

// Create panics because key creation is outside this fixture's supported operations.
func (s *apiKeyRepoStubForGroupUpdate) Create(context.Context, *apikey.APIKey) error {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) GetKeyAndOwnerID(context.Context, int64) (string, int64, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) GetByKey(context.Context, string) (*apikey.APIKey, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) GetByKeyForAuth(context.Context, string) (*apikey.APIKey, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) Delete(context.Context, int64) error { panic("unexpected") }

func (s *apiKeyRepoStubForGroupUpdate) DeleteWithAudit(context.Context, int64) error {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ListByUserID(context.Context, int64, pagination.PaginationParams, apikey.APIKeyListFilters) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) VerifyOwnership(context.Context, int64, []int64) ([]int64, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) CountByUserID(context.Context, int64) (int64, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ExistsByKey(context.Context, string) (bool, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) SearchAPIKeys(context.Context, int64, string, int) ([]apikey.APIKey, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ClearGroupIDByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) CountByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ListKeysByUserID(context.Context, int64) ([]string, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ListKeysByGroupID(context.Context, int64) ([]string, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) IncrementQuotaUsed(context.Context, int64, float64) (float64, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) UpdateLastUsed(context.Context, int64, time.Time) error {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) IncrementRateLimitUsage(context.Context, int64, float64) error {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) ResetRateLimitWindows(context.Context, int64) error {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) GetRateLimitData(context.Context, int64) (*apikey.APIKeyRateLimitData, error) {
	panic("unexpected")
}

func (s *apiKeyRepoStubForGroupUpdate) UpdateGroupIDByUserAndGroup(context.Context, int64, int64, int64) (int64, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) GetByID(_ context.Context, id int64) (*routing.Group, error) {
	s.lastGetByIDArg = id
	if s.getErr != nil {
		return nil, s.getErr
	}
	clone := *s.group
	return &clone, nil
}

// Create panics because group creation is outside this fixture's supported operations.
func (s *groupRepoStubForGroupUpdate) Create(context.Context, *routing.Group) error {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) GetByIDLite(context.Context, int64) (*routing.Group, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) Update(context.Context, *routing.Group) error {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) Delete(context.Context, int64) error { panic("unexpected") }

func (s *groupRepoStubForGroupUpdate) DeleteCascade(context.Context, int64) ([]int64, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) List(context.Context, pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) ListActive(context.Context) ([]routing.Group, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) ListActiveByPlatform(context.Context, string) ([]routing.Group, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) ListActiveByPlatformLite(context.Context, string) ([]routing.Group, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) ExistsByName(context.Context, string) (bool, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) GetProviderCount(context.Context, int64) (int64, int64, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) DeleteProviderGroupsByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) GetProviderIDsByGroupIDs(context.Context, []int64) ([]int64, error) {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) BindProvidersToGroup(context.Context, int64, []int64) error {
	panic("unexpected")
}

func (s *groupRepoStubForGroupUpdate) UpdateSortOrders(context.Context, []routing.GroupSortOrderUpdate) error {
	panic("unexpected")
}

// newKeyAdminForTest uses repository stubs and AdminGroupMutations without a database client.
func newKeyAdminForTest(keys apikey.APIKeyRepository, groups *groupRepoStubForGroupUpdate, users *userRepoStubForGroupUpdate, invalidator *authCacheInvalidatorStub) *apikey.Admin {
	out := &apikey.Admin{Keys: keys, Mutations: &keypostgres.AdminGroupMutations{Keys: keys, Users: users}}
	if users != nil {
		out.Users = users
	}
	if groups != nil {
		out.Groups = keyGroupsFixture{source: groups}
	}
	if invalidator != nil {
		out.Invalidator = invalidator
	}
	return out
}

func (p keyGroupsFixture) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	value, err := p.source.GetByID(ctx, id)
	return apikey.GroupFromRouting(value), err
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}

func int64Ptr(value int64) *int64 { return &value }
