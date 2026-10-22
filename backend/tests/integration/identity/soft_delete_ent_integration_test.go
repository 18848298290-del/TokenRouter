//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	apikeyerrors "github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
)

// Ent queries normally hide soft-deleted keys and subscriptions, while
// SkipSoftDelete exposes their retained rows or enables physical deletion.
// PostgreSQL cases inspect repository and direct Ent reads, repeated deletion,
// hard deletion, and owner subscription lists after one of two grants is
// removed.

// uniqueSoftDeleteValue returns a prefix and test-derived fixture name after
// replacing slashes and spaces with underscores.
func uniqueSoftDeleteValue(t *testing.T, prefix string) string {
	t.Helper()
	safeName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return fmt.Sprintf("%s-%s", prefix, safeName)
}

// createEntUser inserts a user with the supplied email and a fixture password
// hash using ctx. Save failures fail the current test before its key or
// subscription is constructed.
func createEntUser(t *testing.T, ctx context.Context, client *dbent.Client, email string) *dbent.User {
	t.Helper()

	u, err := client.User.Create().
		SetEmail(email).
		SetPasswordHash("test-password-hash").
		Save(ctx)
	require.NoError(t, err, "create ent user")
	return u
}

// TestEntSoftDelete_ApiKey_DefaultFilterAndSkip deletes a committed key through
// its repository. Default repository and Ent reads report absence, while
// SkipSoftDelete returns the row with its deletion timestamp.
func TestEntSoftDelete_ApiKey_DefaultFilterAndSkip(t *testing.T) {
	ctx := context.Background()
	// Use an isolated database to check soft deletion against committed rows.
	integrationDB, client := identityDatabase(t)

	u := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "sd-user")+"@example.com")

	repo := newKeyStoreFixture(client, integrationDB)
	key := &apikeyerrors.APIKey{
		UserID: u.ID,
		Key:    uniqueSoftDeleteValue(t, "sk-soft-delete"),
		Name:   "soft-delete",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key), "create api key")

	require.NoError(t, repo.Delete(ctx, key.ID), "soft delete api key")

	_, err := repo.GetByID(ctx, key.ID)
	require.ErrorIs(t, err, apikeyerrors.ErrAPIKeyNotFound, "deleted rows should be hidden by default")

	_, err = client.APIKey.Query().Where(apikey.IDEQ(key.ID)).Only(ctx)
	require.Error(t, err, "default ent query should not see soft-deleted rows")
	require.True(t, dbent.IsNotFound(err), "expected ent not-found after default soft delete filter")

	got, err := client.APIKey.Query().
		Where(apikey.IDEQ(key.ID)).
		Only(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err, "SkipSoftDelete should include soft-deleted rows")
	require.NotNil(t, got.DeletedAt, "deleted_at should be set after soft delete")
}

// TestEntSoftDelete_ApiKey_DeleteIdempotent creates a committed key and calls
// repository deletion twice. Both calls succeed for that row after the first
// has already soft deleted it.
func TestEntSoftDelete_ApiKey_DeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	// Use an isolated database so repeated deletion observes committed rows.
	integrationDB, client := identityDatabase(t)

	u := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "sd-user2")+"@example.com")

	repo := newKeyStoreFixture(client, integrationDB)
	key := &apikeyerrors.APIKey{
		UserID: u.ID,
		Key:    uniqueSoftDeleteValue(t, "sk-soft-delete2"),
		Name:   "soft-delete2",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key), "create api key")

	require.NoError(t, repo.Delete(ctx, key.ID), "first delete")
	require.NoError(t, repo.Delete(ctx, key.ID), "second delete should be idempotent")
}

// TestEntSoftDelete_ApiKey_RollbackUsesTransactionClient deletes a committed key
// through an Ent transaction. The deletion is visible inside that transaction,
// stays invisible to another connection, and disappears when the transaction rolls back.
func TestEntSoftDelete_ApiKey_RollbackUsesTransactionClient(t *testing.T) {
	ctx := context.Background()
	db, client := identityDatabase(t)
	user := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "rollback-user")+"@example.com")
	key := &apikeyerrors.APIKey{
		UserID: user.ID,
		Key:    uniqueSoftDeleteValue(t, "rollback-key"),
		Name:   "rollback",
		Status: billing.StatusActive,
	}
	require.NoError(t, newKeyStoreFixture(client, db).Create(ctx, key))
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	// Retain rollback cleanup until assertions finish, including early failures,
	// so the independently committed fixture remains available after transaction-local deletion.
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, tx.Client().APIKey.DeleteOneID(key.ID).Exec(ctx))
	_, err = tx.Client().APIKey.Get(ctx, key.ID)
	require.True(t, dbent.IsNotFound(err))
	deleted, err := tx.Client().APIKey.Get(mixins.SkipSoftDelete(ctx), key.ID)
	require.NoError(t, err)
	require.NotNil(t, deleted.DeletedAt)
	outside, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Nil(t, outside.DeletedAt)
	require.NoError(t, tx.Rollback())
	restored, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Nil(t, restored.DeletedAt)
}

// TestEntSoftDelete_ApiKey_HardDeleteViaSkipSoftDelete first soft deletes a
// key, then physically deletes through the bypass context. A bypass query also
// reports absence after the hard deletion.
func TestEntSoftDelete_ApiKey_HardDeleteViaSkipSoftDelete(t *testing.T) {
	ctx := context.Background()
	// Use an isolated database to check physical deletion through SkipSoftDelete.
	integrationDB, client := identityDatabase(t)

	u := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "sd-user3")+"@example.com")

	repo := newKeyStoreFixture(client, integrationDB)
	key := &apikeyerrors.APIKey{
		UserID: u.ID,
		Key:    uniqueSoftDeleteValue(t, "sk-soft-delete3"),
		Name:   "soft-delete3",
		Status: billing.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key), "create api key")

	require.NoError(t, repo.Delete(ctx, key.ID), "soft delete api key")

	// Bypass the soft-delete hook for this physical removal so the following
	// bypass query can distinguish an absent row from a retained row with
	// deleted_at.
	_, err := client.APIKey.Delete().Where(apikey.IDEQ(key.ID)).Exec(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err, "hard delete")

	_, err = client.APIKey.Query().
		Where(apikey.IDEQ(key.ID)).
		Only(mixins.SkipSoftDelete(ctx))
	require.True(t, dbent.IsNotFound(err), "expected row to be hard deleted")
}

// createEntPlan inserts a thirty-day subscription plan for sale using the
// supplied name and context. Save failures fail the test before dependent
// subscription fixture creation continues.
func createEntPlan(t *testing.T, ctx context.Context, client *dbent.Client, name string) *dbent.SubscriptionPlan {
	t.Helper()

	plan, err := client.SubscriptionPlan.Create().
		SetName(name).
		SetDescription("soft delete test plan").
		SetPrice(9.9).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetFeatures("").
		SetProductName("").
		SetForSale(true).
		SetSortOrder(0).
		Save(ctx)
	require.NoError(t, err, "create ent plan")
	return plan
}

// TestEntSoftDelete_UserSubscription_DefaultFilterAndSkip deletes a committed
// subscription through its repository. Default reads hide the grant, while an
// Ent bypass query exposes its row and deletion timestamp.
func TestEntSoftDelete_UserSubscription_DefaultFilterAndSkip(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)

	u := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-user")+"@example.com")
	plan := createEntPlan(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-plan"))

	repo := billingpostgres.NewUserSubscriptionRepository(client)
	sub := &billing.UserSubscription{
		UserID:    u.ID,
		PlanID:    plan.ID,
		Status:    billing.SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, repo.Create(ctx, sub), "create user subscription")

	require.NoError(t, repo.Delete(ctx, sub.ID), "soft delete user subscription")

	_, err := repo.GetByID(ctx, sub.ID)
	require.Error(t, err, "deleted rows should be hidden by default")

	_, err = client.UserSubscription.Query().Where(usersubscription.IDEQ(sub.ID)).Only(ctx)
	require.Error(t, err, "default ent query should not see soft-deleted rows")
	require.True(t, dbent.IsNotFound(err), "expected ent not-found after default soft delete filter")

	got, err := client.UserSubscription.Query().
		Where(usersubscription.IDEQ(sub.ID)).
		Only(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err, "SkipSoftDelete should include soft-deleted rows")
	require.NotNil(t, got.DeletedAt, "deleted_at should be set after soft delete")
}

// TestEntSoftDelete_UserSubscription_DeleteIdempotent creates a committed grant
// for one user and plan, then calls repository deletion twice. Both calls
// succeed after the grant is soft deleted.
func TestEntSoftDelete_UserSubscription_DeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)

	u := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-user2")+"@example.com")
	plan := createEntPlan(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-plan2"))

	repo := billingpostgres.NewUserSubscriptionRepository(client)
	sub := &billing.UserSubscription{
		UserID:    u.ID,
		PlanID:    plan.ID,
		Status:    billing.SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, repo.Create(ctx, sub), "create user subscription")

	require.NoError(t, repo.Delete(ctx, sub.ID), "first delete")
	require.NoError(t, repo.Delete(ctx, sub.ID), "second delete should be idempotent")
}

// TestEntSoftDelete_UserSubscription_ListExcludesDeleted creates two owner
// subscriptions and soft deletes one through storage. Listing by that user
// returns one row with the surviving grant ID.
func TestEntSoftDelete_UserSubscription_ListExcludesDeleted(t *testing.T) {
	ctx := context.Background()
	_, client := identityDatabase(t)

	u := createEntUser(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-user3")+"@example.com")
	plan1 := createEntPlan(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-plan3a"))
	plan2 := createEntPlan(t, ctx, client, uniqueSoftDeleteValue(t, "sd-sub-plan3b"))

	repo := billingpostgres.NewUserSubscriptionRepository(client)

	sub1 := &billing.UserSubscription{
		UserID:    u.ID,
		PlanID:    plan1.ID,
		Status:    billing.SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, repo.Create(ctx, sub1), "create subscription 1")

	sub2 := &billing.UserSubscription{
		UserID:    u.ID,
		PlanID:    plan2.ID,
		Status:    billing.SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, repo.Create(ctx, sub2), "create subscription 2")

	require.NoError(t, repo.Delete(ctx, sub1.ID), "soft delete subscription 1")

	// ListByUserID must exclude the soft-deleted subscription.
	subs, err := repo.ListByUserID(ctx, u.ID)
	require.NoError(t, err, "ListByUserID")
	require.Len(t, subs, 1, "should only return non-deleted subscriptions")
	require.Equal(t, sub2.ID, subs[0].ID, "expected sub2 to be returned")
}
