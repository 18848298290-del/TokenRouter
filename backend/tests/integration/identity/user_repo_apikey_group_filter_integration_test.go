//go:build integration

package identity_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/suite"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Administrator user filtering can depend on the routing group of any
// nondeleted key owned by that user. PostgreSQL cases combine key-group
// selection with user status, check a soft-deleted key, and confirm that
// several keys still produce one owner row. A zero group selection includes
// users with no matching key requirement.
//
// The suite reuses a cloned database and attempts ordered deletion of keys,
// grants, subscriptions, users, and groups before each case. Those cleanup
// errors are discarded, so a rejected delete can leave fixture rows for later
// calls. Repository and Ent helpers share the retained clone, and filtered
// fixture reads request at most fifty users.

// UserRepoAPIKeyGroupFilterSuite retains a PostgreSQL clone for cases combining
// user and key-group filters.
type UserRepoAPIKeyGroupFilterSuite struct {
	// db and client share the clone across sequential cases. SetupTest replaces
	// ctx and repo before attempting fixture deletion, while database cleanup owns
	// connection closure.
	db          *sql.DB             // borrowed suite connection pool
	suite.Suite                     // assertion and suite lifecycle support
	ctx         context.Context     // case execution context
	client      *dbent.Client       // borrowed suite Ent client
	repo        *postgres.UserStore // case user storage fixture
}

// SetupSuite obtains one migrated PostgreSQL clone for the combined user-filter
// cases. The retained pool and Ent client remain usable across the suite and
// are closed by fixture cleanup afterward.
func (s *UserRepoAPIKeyGroupFilterSuite) SetupSuite() { s.db, s.client = identityDatabase(s.T()) }

// SetupTest replaces the user repository and attempts fixture deletion before
// each case reuses fixed emails and credentials. It issues key, grant and
// subscription deletions before user and group deletions so dependent rows can
// be removed first.
//
// Individual deletion errors are discarded, so a failure can leave rows that
// affect later setup or assertions. The retained clone stays open across cases;
// final database cleanup drops the clone after the suite finishes.
func (s *UserRepoAPIKeyGroupFilterSuite) SetupTest() {
	s.ctx = context.Background()

	s.repo = postgres.NewUserStoreWithSQL(s.client, s.db)
	// Attempt dependent key, grant, and subscription deletion before user and
	// group removal so fixture owners can be removed. Discarded errors can leave
	// partial cleanup.
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM api_keys")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM user_allowed_groups")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM user_subscriptions")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM users")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM groups")
}

// TestUserRepoAPIKeyGroupFilterSuite runs user-filter cases in a reused
// PostgreSQL clone. The cases inspect key-group matches, status combinations,
// deleted credentials, duplicate owners, and zero group selection.
func TestUserRepoAPIKeyGroupFilterSuite(t *testing.T) {
	suite.Run(t, new(UserRepoAPIKeyGroupFilterSuite))
}

// mustCreateUser creates an active ordinary user with concurrency five through
// the suite repository and fills its identity. Save failures fail the current
// case before constructing any owned credential fixtures.
func (s *UserRepoAPIKeyGroupFilterSuite) mustCreateUser(email string) *identity.User {
	s.T().Helper()
	u := &identity.User{
		Email:        email,
		PasswordHash: "test-password-hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		Concurrency:  5,
	}
	s.Require().NoError(s.repo.Create(s.ctx, u), "create user")
	return u
}

// mustCreateGroup inserts an active routing group with the supplied name and
// returns its saved Ent entity. Save failures fail the current case, whose
// setup owns fixture row cleanup.
func (s *UserRepoAPIKeyGroupFilterSuite) mustCreateGroup(name string) *dbent.Group {
	s.T().Helper()
	g, err := s.client.Group.Create().
		SetName(name).
		SetStatus(billing.StatusActive).
		Save(s.ctx)
	s.Require().NoError(err, "create group")
	return g
}

// mustCreateAPIKey inserts a key for userID with the supplied credential and
// name using the shared suite client. A nonnil groupID is persisted, and save
// failures fail the current case.
func (s *UserRepoAPIKeyGroupFilterSuite) mustCreateAPIKey(userID int64, key, name string, groupID *int64) *dbent.APIKey {
	s.T().Helper()
	create := s.client.APIKey.Create().
		SetUserID(userID).
		SetKey(key).
		SetName(name)
	if groupID != nil {
		create = create.SetGroupID(*groupID)
	}
	ak, err := create.Save(s.ctx)
	s.Require().NoError(err, "create api key")
	return ak
}

// ids returns a newly allocated slice of input user IDs in their supplied
// order.
func (s *UserRepoAPIKeyGroupFilterSuite) ids(users []identity.User) []int64 {
	out := make([]int64, len(users))
	for i := range users {
		out[i] = users[i].ID
	}
	return out
}

// listByAPIKeyGroup returns the first fifty users matching the supplied
// positive key-group filter through the suite repository. Nonpositive values
// disable that filter, pagination metadata is discarded, and read errors fail
// the case.
func (s *UserRepoAPIKeyGroupFilterSuite) listByAPIKeyGroup(groupID int64) []identity.User {
	s.T().Helper()
	users, _, err := s.repo.ListWithFilters(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 50},
		identity.UserListFilters{APIKeyGroupID: groupID},
	)
	s.Require().NoError(err, "ListWithFilters")
	return users
}

// TestFiltersUsersByAPIKeyGroup gives separate users keys in the target and
// another group. The filtered list contains exactly the owner whose credential
// is assigned to the selected group.
func (s *UserRepoAPIKeyGroupFilterSuite) TestFiltersUsersByAPIKeyGroup() {
	g := s.mustCreateGroup("grp-target")
	other := s.mustCreateGroup("grp-other")
	hit := s.mustCreateUser("hit@test.com")
	miss := s.mustCreateUser("miss@test.com")
	s.mustCreateAPIKey(hit.ID, "sk-hit", "K", &g.ID)
	s.mustCreateAPIKey(miss.ID, "sk-miss", "K", &other.ID)

	s.Require().Equal([]int64{hit.ID}, s.ids(s.listByAPIKeyGroup(g.ID)))
}

// TestSoftDeletedAPIKeyExcluded soft deletes a user's sole key in the selected
// group through Ent. The key-group user query returns no matching owner for
// that deleted credential.
func (s *UserRepoAPIKeyGroupFilterSuite) TestSoftDeletedAPIKeyExcluded() {
	g := s.mustCreateGroup("grp-soft")
	u := s.mustCreateUser("soft@test.com")
	ak := s.mustCreateAPIKey(u.ID, "sk-soft", "K", &g.ID)
	// The soft-delete hook turns Delete into an update of deleted_at.
	s.Require().NoError(s.client.APIKey.DeleteOne(ak).Exec(s.ctx), "soft delete api key")

	s.Require().Empty(s.listByAPIKeyGroup(g.ID), "user with only a soft-deleted key must not match")
}

// TestMultipleKeysAnyMatchDedup gives one user keys in another group, the
// target group, and no group. Filtering by the target returns that owner
// exactly once.
func (s *UserRepoAPIKeyGroupFilterSuite) TestMultipleKeysAnyMatchDedup() {
	g := s.mustCreateGroup("grp-multi")
	other := s.mustCreateGroup("grp-multi-other")
	u := s.mustCreateUser("multi@test.com")
	s.mustCreateAPIKey(u.ID, "sk-m1", "K1", &other.ID)
	s.mustCreateAPIKey(u.ID, "sk-m2", "K2", &g.ID)
	s.mustCreateAPIKey(u.ID, "sk-m3", "K3", nil) // No group

	s.Require().Equal([]int64{u.ID}, s.ids(s.listByAPIKeyGroup(g.ID)))
}

// TestAPIKeyGroupAndStatusFilter supplies active and disabled target owners
// plus an active other-group owner. Combining active status with the key-group
// filter returns just the active target owner.
func (s *UserRepoAPIKeyGroupFilterSuite) TestAPIKeyGroupAndStatusFilter() {
	g := s.mustCreateGroup("grp-combined")

	// This active user has a key in the target group.
	active := s.mustCreateUser("active-hit@test.com")
	s.mustCreateAPIKey(active.ID, "sk-active", "K", &g.ID)

	// This user matches the group but is excluded by the active-status filter.
	disabled := s.mustCreateUser("disabled-hit@test.com")
	s.mustCreateAPIKey(disabled.ID, "sk-disabled", "K2", &g.ID)
	_, err := s.client.User.UpdateOneID(disabled.ID).SetStatus(billing.StatusDisabled).Save(s.ctx)
	s.Require().NoError(err, "disable user")

	// This active user is excluded by the group filter.
	other := s.mustCreateGroup("grp-combined-other")
	miss := s.mustCreateUser("active-miss@test.com")
	s.mustCreateAPIKey(miss.ID, "sk-miss", "K3", &other.ID)

	users, _, err := s.repo.ListWithFilters(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 50},
		identity.UserListFilters{
			APIKeyGroupID: g.ID,
			Status:        billing.StatusActive,
		},
	)
	s.Require().NoError(err)
	s.Require().Equal([]int64{active.ID}, s.ids(users), "only active user with matching key group should match")
}

// TestZeroGroupIDNoFilter supplies zero as the key-group selection with one
// keyed and one unkeyed user. Both owner IDs appear in the returned user list.
func (s *UserRepoAPIKeyGroupFilterSuite) TestZeroGroupIDNoFilter() {
	g := s.mustCreateGroup("grp-zero")
	u1 := s.mustCreateUser("z1@test.com")
	u2 := s.mustCreateUser("z2@test.com")
	s.mustCreateAPIKey(u1.ID, "sk-z1", "K", &g.ID)

	users, _, err := s.repo.ListWithFilters(
		s.ctx,
		pagination.PaginationParams{Page: 1, PageSize: 50},
		identity.UserListFilters{APIKeyGroupID: 0},
	)
	s.Require().NoError(err)
	s.Require().ElementsMatch([]int64{u1.ID, u2.ID}, s.ids(users))
}
