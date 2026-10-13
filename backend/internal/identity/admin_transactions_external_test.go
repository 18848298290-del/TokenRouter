package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// deleteUserKeysStub records Key read/delete requests during database-free
// account deletion scenarios.
type deleteUserKeysStub struct {
	// AdminKeyReader remains nil for unused methods. Tests override List;
	// another promoted reader call would panic rather than silently simulate
	// repository behavior.
	identity.AdminKeyReader
	// AdminKeyParticipant remains nil for unused mutations. Tests override
	// DeleteWithAudit; a group migration call would panic and reveal an
	// unexpected path.
	identity.AdminKeyParticipant
	// listByUserIDKeys supplies the complete fixture listing. List copies its
	// outer slice while retaining each observation's pointer fields for
	// sequential assertions.
	listByUserIDKeys  []identity.AdminKeySummary
	listByUserIDCalls []int64 // observed listing account identifiers
	deletedIDs        []int64 // observed deletion Key identifiers
}

// deleteUserInvalidator records account and credential callbacks after
// successful deletion returns.
type deleteUserInvalidator struct {
	userIDs []int64  // requested account invalidation identifiers
	keys    []string // requested credential invalidation values
}

// TestAdminService_DeleteUser_Success supplies an ordinary account and a
// database-free mutation fixture. It checks that deletion delegates the
// account ID to the repository and returns success.
func TestAdminService_DeleteUser_Success(t *testing.T) {
	repo := &userRepoStub{user: &identity.User{ID: 7, Role: identity.RoleUser}}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, repo.deletedIDs)
}

// TestAdminService_DeleteUser_DeletesOwnedAPIKeys supplies two account Key
// observations to database-free deletion. It checks listed/deleted IDs and
// post-success account/credential invalidation calls, rather than database
// rollback behavior.
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

// List records the requested account ID and returns a copied slice of every
// configured Key observation. Pagination/sort arguments are ignored and
// pointer fields remain shared, so these fixtures do not model stable
// multi-page database scans.
func (s *deleteUserKeysStub) List(_ context.Context, userID int64, page, pageSize int, _, _ string) ([]identity.AdminKeySummary, int64, error) {
	s.listByUserIDCalls = append(s.listByUserIDCalls, userID)
	keys := append([]identity.AdminKeySummary(nil), s.listByUserIDKeys...)
	return keys, int64(len(keys)), nil
}

// DeleteWithAudit records the Key ID and returns fixture success for each
// call. It changes no stored Key state, allowing tests to observe
// transaction-adapter delegation rather than actual deletion or audit
// persistence.
func (s *deleteUserKeysStub) DeleteWithAudit(_ context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}

// TestAdminService_DeleteUser_NotFound configures the initial account lookup
// to return ErrUserNotFound. It requires that error and verifies no account
// deletion call was attempted.
func TestAdminService_DeleteUser_NotFound(t *testing.T) {
	repo := &userRepoStub{getErr: identity.ErrUserNotFound}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 404)
	require.ErrorIs(t, err, identity.ErrUserNotFound)
	require.Empty(t, repo.deletedIDs)
}

// TestAdminService_DeleteUser_AdminGuard supplies an administrator account to
// the deletion entry. It checks the administrator rejection and verifies that
// the account deletion fixture remains unused.
func TestAdminService_DeleteUser_AdminGuard(t *testing.T) {
	repo := &userRepoStub{user: &identity.User{ID: 1, Role: identity.RoleAdmin}}
	svc := newDeleteUserAdminForTest(repo, nil, nil)

	err := svc.DeleteUser(context.Background(), 1)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot delete admin user")
	require.Empty(t, repo.deletedIDs)
}

// TestAdminService_DeleteUser_DeleteError records an attempted ordinary
// account deletion that returns a configured error. It checks propagation of
// that error and the observed target ID.
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

// newDeleteUserAdminForTest assembles deletion with one optional Key stub for
// both reads and writes and an optional invalidator. AdminMutations has no
// database connection, so its recorded sequential calls cannot establish
// transactional rollback of account or Key state.
func newDeleteUserAdminForTest(users identity.UserRepository, keys *deleteUserKeysStub, invalidator identity.AdminInvalidator) *identity.UserAdmin {
	d := identity.AdminDependencies{Users: users, Invalidator: invalidator, Transactions: &identitypostgres.AdminMutations{Users: users, Keys: keys}}
	if keys != nil {
		d.Keys = keys
	}
	return identity.NewUserAdmin(d)
}

// InvalidateAuthCacheByUserID records the account whose authentication
// invalidation was requested.
func (s *deleteUserInvalidator) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	s.userIDs = append(s.userIDs, id)
}

// InvalidateAuthCacheByKey records the credential whose authentication
// invalidation was requested.
func (s *deleteUserInvalidator) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}
