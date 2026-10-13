//go:build integration

package identity_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Administrator reads can include retained user rows after soft deletion,
// whereas ordinary reads hide those users. PostgreSQL transaction fixtures
// compare filtered row counts and deletion timestamps for IncludeDeleted lists
// and contrast normal ID lookup with the include-deleted detail method.

// TestUserRepo_ListWithFilters_IncludeDeleted stores active and deleted users
// sharing a search fragment. Ordinary listing returns one row and total one;
// IncludeDeleted returns two rows and exposes the deletion timestamp.
func TestUserRepo_ListWithFilters_IncludeDeleted(t *testing.T) {
	ctx := context.Background()
	integrationDB, root := identityDatabase(t)
	tx, err := root.Tx(ctx)
	require.NoError(t, err)
	// Retain seeded users and their soft-deletion writes inside the outer
	// transaction for comparison reads. Rollback cleanup discards those changes
	// before the cloned database closes and ignores its own error.
	t.Cleanup(func() { _ = tx.Rollback() })
	client := tx.Client()
	repo := postgres.NewUserStore(client, integrationDB)

	active := mustCreateUser(t, client, &identity.User{Email: "shared-keyword-active@test.com"})
	deleted := mustCreateUser(t, client, &identity.User{Email: "shared-keyword-deleted@test.com"})
	require.NoError(t, client.User.DeleteOneID(deleted.ID).Exec(ctx))

	params := pagination.PaginationParams{Page: 1, PageSize: 50, SortBy: "email", SortOrder: "asc"}

	// The default query excludes soft-deleted users.
	usersDefault, resDefault, err := repo.ListWithFilters(ctx, params,
		identity.UserListFilters{Search: "shared-keyword-"})
	require.NoError(t, err)
	require.Len(t, usersDefault, 1)
	require.Equal(t, active.ID, usersDefault[0].ID)
	require.EqualValues(t, 1, resDefault.Total)

	// IncludeDeleted includes both rows in the result and total count.
	usersAll, resAll, err := repo.ListWithFilters(ctx, params,
		identity.UserListFilters{Search: "shared-keyword-", IncludeDeleted: true})
	require.NoError(t, err)
	require.Len(t, usersAll, 2)
	require.EqualValues(t, 2, resAll.Total, "Count 必须与结果集行数一致")

	var delUser *identity.User
	for i := range usersAll {
		if usersAll[i].ID == deleted.ID {
			delUser = &usersAll[i]
		}
	}
	require.NotNil(t, delUser)
	require.NotNil(t, delUser.DeletedAt)
}

// TestUserRepo_GetByIDIncludeDeleted soft deletes a created user inside its
// fixture transaction. Normal lookup reports UserNotFound, while the
// include-deleted method returns its retained email and deletion timestamp.
func TestUserRepo_GetByIDIncludeDeleted(t *testing.T) {
	ctx := context.Background()
	integrationDB, root := identityDatabase(t)
	tx, err := root.Tx(ctx)
	require.NoError(t, err)
	// Retain seeded users and their soft-deletion writes inside the outer
	// transaction for comparison reads. Rollback cleanup discards those changes
	// before the cloned database closes and ignores its own error.
	t.Cleanup(func() { _ = tx.Rollback() })
	client := tx.Client()
	repo := postgres.NewUserStore(client, integrationDB)

	u := mustCreateUser(t, client, &identity.User{Email: "getbyid-deleted@test.com"})
	require.NoError(t, client.User.DeleteOneID(u.ID).Exec(ctx))

	// GetByID applies the soft-delete filter.
	_, err = repo.GetByID(ctx, u.ID)
	require.ErrorIs(t, err, identity.ErrUserNotFound)

	// GetByIDIncludeDeleted returns the row and its deletion timestamp.
	got, err := repo.GetByIDIncludeDeleted(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "getbyid-deleted@test.com", got.Email)
	require.NotNil(t, got.DeletedAt)
}
