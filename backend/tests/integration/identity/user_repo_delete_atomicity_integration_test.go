//go:build integration

package identity_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// User deletion and credential tombstoning can join an Ent transaction carried
// in context even when stores retain the base client. The PostgreSQL scenario
// first rolls back both writes and checks visible user and key rows, then
// commits the same deletion sequence and checks that queries hide both
// resources and retain no deleted-key audit rows.

// TestUserRepository_DeleteUser_AtomicWithAPIKeys supplies an outer context
// transaction to base-client stores for user and key deletion. Rollback retains
// visible rows, commit hides them, and neither path leaves deleted-key audits.
func TestUserRepository_DeleteUser_AtomicWithAPIKeys(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)

	userRepo := postgres.NewUserStore(client, integrationDB)
	apiKeyRepo := newKeyStoreFixture(client, integrationDB)

	user := mustCreateUser(t, client, &identity.User{})
	key1 := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-atomic-a-%d", user.ID)})
	key2 := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-atomic-b-%d", user.ID)})

	t.Cleanup(func() {
		// Attempt hard removal of legacy audits and dependent keys before their user
		// owner. Cleanup errors are discarded, and normal progress completes both
		// outer transactions first.
		_, _ = integrationDB.Exec(`DELETE FROM deleted_api_key_audits WHERE user_id = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM api_keys WHERE user_id = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, user.ID)
	})

	listParams := pagination.PaginationParams{Page: 1, PageSize: 10}

	tx, err := client.Tx(ctx)
	require.NoError(t, err, "开启外层事务")
	// Carry the outer transaction through context while stores retain their base
	// client so this sequence exercises context participation rather than
	// transaction-client reuse.
	opCtx := dbent.NewTxContext(ctx, tx)

	require.NoError(t, apiKeyRepo.DeleteWithAudit(opCtx, key1.ID))
	require.NoError(t, apiKeyRepo.DeleteWithAudit(opCtx, key2.ID))
	require.NoError(t, userRepo.Delete(opCtx, user.ID))

	// Roll back after all three deletion calls, then read through the base stores
	// to reveal any key or user write committed outside this chosen transaction.
	require.NoError(t, tx.Rollback(), "回滚外层事务")

	gotUser, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err, "回滚后用户必须仍存在")
	require.Equal(t, user.ID, gotUser.ID)

	keys, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, listParams, apikey.APIKeyListFilters{})
	require.NoError(t, err, "查询回滚后的密钥")
	require.Len(t, keys, 2, "回滚后密钥必须仍为可用状态")

	var auditCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deleted_api_key_audits WHERE user_id = $1`, user.ID).Scan(&auditCount))
	require.Zero(t, auditCount, "回滚后不应留下审计记录")

	tx2, err := client.Tx(ctx)
	require.NoError(t, err, "再次开启外层事务")
	opCtx2 := dbent.NewTxContext(ctx, tx2)

	require.NoError(t, apiKeyRepo.DeleteWithAudit(opCtx2, key1.ID))
	require.NoError(t, apiKeyRepo.DeleteWithAudit(opCtx2, key2.ID))
	require.NoError(t, userRepo.Delete(opCtx2, user.ID))

	// Commit the repeated deletion sequence before its external visibility checks;
	// the same base-client stores should then hide both the user and its
	// credential rows.
	require.NoError(t, tx2.Commit(), "提交外层事务")

	_, err = userRepo.GetByID(ctx, user.ID)
	require.Error(t, err, "提交后用户应被软删除")

	keysAfter, _, err := apiKeyRepo.ListByUserID(ctx, user.ID, listParams, apikey.APIKeyListFilters{})
	require.NoError(t, err, "查询提交后的密钥")
	require.Empty(t, keysAfter, "提交后密钥应全部被软删除")

	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deleted_api_key_audits WHERE user_id = $1`, user.ID).Scan(&auditCount))
	require.Zero(t, auditCount, "提交后也不得保留被删 Key 的凭据材料")
}
