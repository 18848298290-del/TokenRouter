package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// deleteUserKeysStub records key listing and deletion during user deletion.
type deleteUserKeysStub struct {
	identity.AdminKeyReader
	identity.AdminKeyParticipant
	listByUserIDKeys  []identity.AdminKeySummary
	listByUserIDCalls []int64
	deletedIDs        []int64
}

// deleteUserInvalidator records user and key cache invalidations.
type deleteUserInvalidator struct {
	userIDs []int64
	keys    []string
}

func TestAdminService_DeleteUser_Success(t *testing.T) {
	repo := &userRepoStub{user: &identity.User{ID: 7, Role: identity.RoleUser}}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, repo.deletedIDs)
}

func TestAdminService_DeleteUser_DeletesOwnedAPIKeys(t *testing.T) {
	repo := &userRepoStub{user: &identity.User{ID: 7, Role: identity.RoleUser}}
	apiKeyRepo := &deleteUserKeysStub{
		listByUserIDKeys: []identity.AdminKeySummary{
			{ID: 11, Key: "sk-user-1"},
			{ID: 12, Key: "sk-user-2"},
		},
	}
	invalidator := &deleteUserInvalidator{}
	svc := newDeleteUserAdminForTest(repo, apiKeyRepo, invalidator)

	err := svc.DeleteUser(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, repo.deletedIDs)
	require.Equal(t, []int64{7}, apiKeyRepo.listByUserIDCalls)
	require.Equal(t, []int64{11, 12}, apiKeyRepo.deletedIDs)
	require.ElementsMatch(t, []string{"sk-user-1", "sk-user-2"}, invalidator.keys)
	require.Equal(t, []int64{7}, invalidator.userIDs)
}

func (s *deleteUserKeysStub) List(_ context.Context, userID int64, page, pageSize int, _, _ string) ([]identity.AdminKeySummary, int64, error) {
	s.listByUserIDCalls = append(s.listByUserIDCalls, userID)
	keys := append([]identity.AdminKeySummary(nil), s.listByUserIDKeys...)
	return keys, int64(len(keys)), nil
}

func (s *deleteUserKeysStub) DeleteWithAudit(_ context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}

func TestAdminService_DeleteUser_NotFound(t *testing.T) {
	repo := &userRepoStub{getErr: identity.ErrUserNotFound}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 404)
	require.ErrorIs(t, err, identity.ErrUserNotFound)
	require.Empty(t, repo.deletedIDs)
}

func TestAdminService_DeleteUser_AdminGuard(t *testing.T) {
	repo := &userRepoStub{user: &identity.User{ID: 1, Role: identity.RoleAdmin}}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 1)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot delete admin user")
	require.Empty(t, repo.deletedIDs)
}

func TestAdminService_DeleteUser_DeleteError(t *testing.T) {
	deleteErr := errors.New("delete failed")
	repo := &userRepoStub{
		user:      &identity.User{ID: 9, Role: identity.RoleUser},
		deleteErr: deleteErr,
	}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 9)
	require.ErrorIs(t, err, deleteErr)
	require.Equal(t, []int64{9}, repo.deletedIDs)
}

// newDeleteUserAdminForTest shares one key stub between reads and writes and uses transactions without a database connection.
func newDeleteUserAdminForTest(users identity.UserRepository, keys *deleteUserKeysStub, invalidator identity.AdminInvalidator) *identity.UserAdmin {
	d := identity.AdminDependencies{Users: users, Invalidator: invalidator, Transactions: &identitypostgres.AdminMutations{Users: users, Keys: keys}}
	if keys != nil {
		d.Keys = keys
	}
	return identity.NewUserAdmin(d)
}

func (s *deleteUserInvalidator) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	s.userIDs = append(s.userIDs, id)
}

func (s *deleteUserInvalidator) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}
