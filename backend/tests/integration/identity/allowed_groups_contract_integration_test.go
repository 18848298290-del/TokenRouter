//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Removing an allowed-group grant affects every user row that names the target
// group, while deleting a group retains a key's stored group ID. PostgreSQL
// transaction cases inspect removed user grants, remaining unrelated grants,
// hidden deleted groups, and a nil loaded group on the still-bound key.

// uniqueTestValue returns prefix plus the test name with slashes and spaces
// replaced by underscores.
func uniqueTestValue(t *testing.T, prefix string) string {
	t.Helper()
	safeName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return fmt.Sprintf("%s-%s", prefix, safeName)
}

// TestUserRepository_RemoveGroupFromAllowedGroups_RemovesAllOccurrences grants
// a target group to two users and another group to a third. Removal reports two
// affected users and retains the first user's other grant.
func TestUserRepository_RemoveGroupFromAllowedGroups_RemovesAllOccurrences(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Keep all seeded grants and deletion observations inside this transaction;
	// cleanup rolls them back before the cloned database closes, discarding
	// repeated-completion errors.
	t.Cleanup(func() { _ = tx.Rollback() })
	entClient := tx.Client()

	targetGroup, err := entClient.Group.Create().
		SetName(uniqueTestValue(t, "target-group")).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	otherGroup, err := entClient.Group.Create().
		SetName(uniqueTestValue(t, "other-group")).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	repo := postgres.NewUserStoreWithSQL(entClient, tx)

	u1 := &identity.User{
		Email:         uniqueTestValue(t, "u1") + "@example.com",
		PasswordHash:  "test-password-hash",
		Role:          identity.RoleUser,
		Status:        billing.StatusActive,
		Concurrency:   5,
		AllowedGroups: []int64{targetGroup.ID, otherGroup.ID},
	}
	require.NoError(t, repo.Create(ctx, u1))

	u2 := &identity.User{
		Email:         uniqueTestValue(t, "u2") + "@example.com",
		PasswordHash:  "test-password-hash",
		Role:          identity.RoleUser,
		Status:        billing.StatusActive,
		Concurrency:   5,
		AllowedGroups: []int64{targetGroup.ID},
	}
	require.NoError(t, repo.Create(ctx, u2))

	u3 := &identity.User{
		Email:         uniqueTestValue(t, "u3") + "@example.com",
		PasswordHash:  "test-password-hash",
		Role:          identity.RoleUser,
		Status:        billing.StatusActive,
		Concurrency:   5,
		AllowedGroups: []int64{otherGroup.ID},
	}
	require.NoError(t, repo.Create(ctx, u3))

	affected, err := repo.RemoveGroupFromAllowedGroups(ctx, targetGroup.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), affected)

	u1After, err := repo.GetByID(ctx, u1.ID)
	require.NoError(t, err)
	require.NotContains(t, u1After.AllowedGroups, targetGroup.ID)
	require.Contains(t, u1After.AllowedGroups, otherGroup.ID)

	u2After, err := repo.GetByID(ctx, u2.ID)
	require.NoError(t, err)
	require.NotContains(t, u2After.AllowedGroups, targetGroup.ID)
}

// TestGroupRepository_DeleteCascade_PreservesApiKeyGroupID deletes a group used
// by a key and user grant. Reads hide the group and remove its grant while the
// key retains GroupID with a nil group.
func TestGroupRepository_DeleteCascade_PreservesApiKeyGroupID(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Keep all seeded grants and deletion observations inside this transaction;
	// cleanup rolls them back before the cloned database closes, discarding
	// repeated-completion errors.
	t.Cleanup(func() { _ = tx.Rollback() })
	entClient := tx.Client()

	targetGroup, err := entClient.Group.Create().
		SetName(uniqueTestValue(t, "delete-cascade-target")).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	otherGroup, err := entClient.Group.Create().
		SetName(uniqueTestValue(t, "delete-cascade-other")).
		SetStatus(billing.StatusActive).
		Save(ctx)
	require.NoError(t, err)

	userRepo := postgres.NewUserStoreWithSQL(entClient, tx)
	groupRepo := newGroupStoreFixture(entClient, tx)
	apiKeyRepo := newKeyStoreFixture(entClient, tx)

	u := &identity.User{
		Email:         uniqueTestValue(t, "cascade-user") + "@example.com",
		PasswordHash:  "test-password-hash",
		Role:          identity.RoleUser,
		Status:        billing.StatusActive,
		Concurrency:   5,
		AllowedGroups: []int64{targetGroup.ID, otherGroup.ID},
	}
	require.NoError(t, userRepo.Create(ctx, u))

	key := &apikey.APIKey{
		UserID:  u.ID,
		Key:     uniqueTestValue(t, "sk-test-delete-cascade"),
		Name:    "test key",
		GroupID: &targetGroup.ID,
		Status:  billing.StatusActive,
	}
	require.NoError(t, apiKeyRepo.Create(ctx, key))

	_, err = groupRepo.DeleteCascade(ctx, targetGroup.ID)
	require.NoError(t, err)

	// Default queries hide soft-deleted groups.
	_, err = groupRepo.GetByID(ctx, targetGroup.ID)
	require.ErrorIs(t, err, routing.ErrGroupNotFound)

	activeGroups, err := groupRepo.ListActive(ctx)
	require.NoError(t, err)
	for _, g := range activeGroups {
		require.NotEqual(t, targetGroup.ID, g.ID)
	}

	// Deleted groups must be absent from the user's available groups.
	uAfter, err := userRepo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.NotContains(t, uAfter.AllowedGroups, targetGroup.ID)
	require.Contains(t, uAfter.AllowedGroups, otherGroup.ID)

	// Retain the stored group ID while the loaded group disappears, leaving the
	// deleted binding identifiable when authentication later evaluates this key's
	// group availability.
	keyAfter, err := apiKeyRepo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.NotNil(t, keyAfter.GroupID)
	require.Equal(t, targetGroup.ID, *keyAfter.GroupID)
	require.Nil(t, keyAfter.Group)
}
