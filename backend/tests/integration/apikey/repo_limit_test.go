package apikey_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// A positive user key limit counts active and disabled credentials until their
// rows are soft deleted. Sequential SQLite cases check that counting rule, zero
// as unlimited, and the behavior of existing keys after an administrator lowers
// the limit.

// TestAPIKeyRepository_CreateEnforcesUserLimitAcrossStatuses fills a two-key
// allowance with active and disabled rows. A third creation reports a conflict
// with current and limit metadata both equal to two.
func TestAPIKeyRepository_CreateEnforcesUserLimitAcrossStatuses(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-limit-status@test.com")
	require.NoError(t, client.User.UpdateOneID(user.ID).SetAPIKeyLimit(2).Exec(ctx))

	require.NoError(t, repo.Create(ctx, newLimitedAPIKey(user.ID, "sk-limit-active", apikey.StatusAPIKeyActive)))
	require.NoError(t, repo.Create(ctx, newLimitedAPIKey(user.ID, "sk-limit-disabled", apikey.StatusAPIKeyDisabled)))

	err := repo.Create(ctx, newLimitedAPIKey(user.ID, "sk-limit-rejected", apikey.StatusAPIKeyActive))
	require.ErrorIs(t, err, apikey.ErrAPIKeyLimitReached)
	require.Equal(t, 409, httpx.ErrorCode(err))
	appErr := apperror.FromError(err)
	require.Equal(t, "2", appErr.Metadata["current"])
	require.Equal(t, "2", appErr.Metadata["limit"])
}

// TestAPIKeyRepository_CreateReleasesSlotAfterSoftDelete fills a one-key
// allowance and then soft deletes that key. A subsequent creation succeeds with
// the deleted row excluded from the limit.
func TestAPIKeyRepository_CreateReleasesSlotAfterSoftDelete(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-limit-delete@test.com")
	require.NoError(t, client.User.UpdateOneID(user.ID).SetAPIKeyLimit(1).Exec(ctx))

	first := newLimitedAPIKey(user.ID, "sk-limit-delete-first", apikey.StatusAPIKeyActive)
	require.NoError(t, repo.Create(ctx, first))
	require.NoError(t, repo.Delete(ctx, first.ID))
	require.NoError(t, repo.Create(ctx, newLimitedAPIKey(user.ID, "sk-limit-delete-second", apikey.StatusAPIKeyActive)))
}

// TestAPIKeyRepository_CreateAllowsUnlimitedAndPreservesExistingKeysAfterLowering
// creates three keys with a zero limit. Lowering the limit to two blocks
// another creation while the count remains three.
func TestAPIKeyRepository_CreateAllowsUnlimitedAndPreservesExistingKeysAfterLowering(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "api-key-limit-unlimited@test.com")
	require.NoError(t, client.User.UpdateOneID(user.ID).SetAPIKeyLimit(0).Exec(ctx))

	for _, key := range []string{"sk-limit-unlimited-1", "sk-limit-unlimited-2", "sk-limit-unlimited-3"} {
		require.NoError(t, repo.Create(ctx, newLimitedAPIKey(user.ID, key, apikey.StatusAPIKeyActive)))
	}
	require.NoError(t, client.User.UpdateOneID(user.ID).SetAPIKeyLimit(2).Exec(ctx))

	err := repo.Create(ctx, newLimitedAPIKey(user.ID, "sk-limit-after-lowering", apikey.StatusAPIKeyActive))
	require.ErrorIs(t, err, apikey.ErrAPIKeyLimitReached)
	count, countErr := repo.CountByUserID(ctx, user.ID)
	require.NoError(t, countErr)
	require.Equal(t, int64(3), count)
}
