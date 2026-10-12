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

type UserRepoAPIKeyGroupFilterSuite struct {
	db *sql.DB
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	repo   *postgres.UserStore
}

// SetupSuite creates an isolated database for combined user filters.
func (s *UserRepoAPIKeyGroupFilterSuite) SetupSuite() { s.db, s.client = identityDatabase(s.T()) }

func (s *UserRepoAPIKeyGroupFilterSuite) SetupTest() {
	s.ctx = context.Background()

	s.repo = postgres.NewUserStoreWithSQL(s.client, s.db)
	// Delete API Keys before users to satisfy foreign keys, and clear groups between cases.
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM api_keys")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM user_allowed_groups")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM user_subscriptions")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM users")
	_, _ = s.db.ExecContext(s.ctx, "DELETE FROM groups")
}

func TestUserRepoAPIKeyGroupFilterSuite(t *testing.T) {
	suite.Run(t, new(UserRepoAPIKeyGroupFilterSuite))
}

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

func (s *UserRepoAPIKeyGroupFilterSuite) mustCreateGroup(name string) *dbent.Group {
	s.T().Helper()
	g, err := s.client.Group.Create().
		SetName(name).
		SetStatus(billing.StatusActive).
		Save(s.ctx)
	s.Require().NoError(err, "create group")
	return g
}

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

func (s *UserRepoAPIKeyGroupFilterSuite) ids(users []identity.User) []int64 {
	out := make([]int64, len(users))
	for i := range users {
		out[i] = users[i].ID
	}
	return out
}

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

// TestFiltersUsersByAPIKeyGroup selects users whose keys belong to the requested group.
func (s *UserRepoAPIKeyGroupFilterSuite) TestFiltersUsersByAPIKeyGroup() {
	g := s.mustCreateGroup("grp-target")
	other := s.mustCreateGroup("grp-other")
	hit := s.mustCreateUser("hit@test.com")
	miss := s.mustCreateUser("miss@test.com")
	s.mustCreateAPIKey(hit.ID, "sk-hit", "K", &g.ID)
	s.mustCreateAPIKey(miss.ID, "sk-miss", "K", &other.ID)

	s.Require().Equal([]int64{hit.ID}, s.ids(s.listByAPIKeyGroup(g.ID)))
}

// TestSoftDeletedAPIKeyExcluded checks the subquery's explicit deleted_at filter.
func (s *UserRepoAPIKeyGroupFilterSuite) TestSoftDeletedAPIKeyExcluded() {
	g := s.mustCreateGroup("grp-soft")
	u := s.mustCreateUser("soft@test.com")
	ak := s.mustCreateAPIKey(u.ID, "sk-soft", "K", &g.ID)
	// The soft-delete hook turns Delete into an update of deleted_at.
	s.Require().NoError(s.client.APIKey.DeleteOne(ak).Exec(s.ctx), "soft delete api key")

	s.Require().Empty(s.listByAPIKeyGroup(g.ID), "user with only a soft-deleted key must not match")
}

// TestMultipleKeysAnyMatchDedup checks that one matching key returns the user once.
func (s *UserRepoAPIKeyGroupFilterSuite) TestMultipleKeysAnyMatchDedup() {
	g := s.mustCreateGroup("grp-multi")
	other := s.mustCreateGroup("grp-multi-other")
	u := s.mustCreateUser("multi@test.com")
	s.mustCreateAPIKey(u.ID, "sk-m1", "K1", &other.ID)
	s.mustCreateAPIKey(u.ID, "sk-m2", "K2", &g.ID)
	s.mustCreateAPIKey(u.ID, "sk-m3", "K3", nil) // No group

	s.Require().Equal([]int64{u.ID}, s.ids(s.listByAPIKeyGroup(g.ID)))
}

// TestAPIKeyGroupAndStatusFilter requires both group ownership and account status to match.
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

// TestZeroGroupIDNoFilter checks that zero leaves users unfiltered by API Key group.
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
