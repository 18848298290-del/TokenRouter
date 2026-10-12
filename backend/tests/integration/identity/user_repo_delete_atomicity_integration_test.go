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

// TestUserRepository_DeleteUser_AtomicWithAPIKeys checks that user deletion and key
// audit/deletion share the outer transaction supplied through context.
// Rolling back that transaction must restore both the user and its keys.
//
// Construct stores with the base client, then begin the outer transaction here,
// matching application wiring. A store built from a transaction client would
// exercise the ErrTxStarted reuse path instead of context transaction reuse.
func TestUserRepository_DeleteUser_AtomicWithAPIKeys(t *testing.T) {
	ctx := context.Background()
	integrationDB, client := identityDatabase(t)

	userRepo := postgres.NewUserStore(client, integrationDB)
	apiKeyRepo := newKeyStoreFixture(client, integrationDB)

	user := mustCreateUser(t, client, &identity.User{})
	key1 := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-atomic-a-%d", user.ID)})
	key2 := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: fmt.Sprintf("sk-atomic-b-%d", user.ID)})

	t.Cleanup(func() {
		// Clean committed fixture rows and key audits after the test.
		_, _ = integrationDB.Exec(`DELETE FROM deleted_api_key_audits WHERE user_id = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM api_keys WHERE user_id = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, user.ID)
	})

	listParams := pagination.PaginationParams{Page: 1, PageSize: 10}

	tx, err := client.Tx(ctx)
	require.NoError(t, err, "开启外层事务")
	opCtx := dbent.NewTxContext(ctx, tx)

	require.NoError(t, apiKeyRepo.DeleteWithAudit(opCtx, key1.ID))
	require.NoError(t, apiKeyRepo.DeleteWithAudit(opCtx, key2.ID))
	require.NoError(t, userRepo.Delete(opCtx, user.ID))

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
