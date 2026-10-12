//go:build integration

package promotion_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	promotionpostgres "github.com/TokenFlux/TokenRouter/internal/promotion/postgres"
)

func querySingleFloat(t *testing.T, ctx context.Context, client *dbent.Client, query string, args ...any) float64 {
	t.Helper()
	rows, err := client.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next(), "expected one row")
	var value float64
	require.NoError(t, rows.Scan(&value))
	require.NoError(t, rows.Err())
	return value
}

func querySingleInt(t *testing.T, ctx context.Context, client *dbent.Client, query string, args ...any) int {
	t.Helper()
	rows, err := client.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next(), "expected one row")
	var value int
	require.NoError(t, rows.Scan(&value))
	require.NoError(t, rows.Err())
	return value
}

func TestAffiliateRepository_TransferQuotaToBalance_UsesClaimedQuotaBeforeClear(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })

	u := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-transfer-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		Balance:      5.5,
		Concurrency:  5,
	})

	affCode := fmt.Sprintf("AFF%09d", time.Now().UnixNano()%1_000_000_000)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, $3, $3, NOW(), NOW())`, u.ID, affCode, 12.34)
	require.NoError(t, err)

	transferred, balance, err := repo.TransferQuotaToBalance(txCtx, u.ID)
	require.NoError(t, err)
	require.InDelta(t, 12.34, transferred, 1e-9)
	require.InDelta(t, 17.84, balance, 1e-9)

	affQuota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", u.ID)
	require.InDelta(t, 0.0, affQuota, 1e-9)

	persistedBalance := querySingleFloat(t, txCtx, client,
		"SELECT balance::double precision FROM users WHERE id = $1", u.ID)
	require.InDelta(t, 17.84, persistedBalance, 1e-9)

	ledgerCount := querySingleInt(t, txCtx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1 AND action = 'transfer'", u.ID)
	require.Equal(t, 1, ledgerCount)

	rows, err := client.QueryContext(txCtx, `
SELECT amount::double precision,
       balance_after::double precision,
       aff_quota_after::double precision,
       aff_frozen_quota_after::double precision,
       aff_history_quota_after::double precision
FROM user_affiliate_ledger
WHERE user_id = $1 AND action = 'transfer'
LIMIT 1`, u.ID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next(), "expected transfer ledger")
	var amount, balanceAfter, quotaAfter, frozenAfter, historyAfter float64
	require.NoError(t, rows.Scan(&amount, &balanceAfter, &quotaAfter, &frozenAfter, &historyAfter))
	require.InDelta(t, 12.34, amount, 1e-9)
	require.InDelta(t, 17.84, balanceAfter, 1e-9)
	require.InDelta(t, 0.0, quotaAfter, 1e-9)
	require.InDelta(t, 0.0, frozenAfter, 1e-9)
	require.InDelta(t, 12.34, historyAfter, 1e-9)
}

// TestAffiliateRepository_AccrueQuota_ReusesOuterTransaction checks reuse of the transaction passed through
// dbent.NewTxContext.
// Rolling it back removes the affiliate profiles created in the transaction and their accrual.
func TestAffiliateRepository_AccrueQuota_ReusesOuterTransaction(t *testing.T) {
	ctx := context.Background()

	integrationEntClient, _ := testStore(t)
	outerTx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err, "begin outer tx")
	// Roll back even if an assertion stops the test early; a second rollback error is harmless.
	t.Cleanup(func() { _ = outerTx.Rollback() })
	client := outerTx.Client()
	txCtx := dbent.NewTxContext(ctx, outerTx)

	inviter := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-inviter-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		Concurrency:  5,
	})
	invitee := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-invitee-%d@example.com", time.Now().UnixNano()+1),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		Concurrency:  5,
	})

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })
	_, err = repo.EnsureUserAffiliate(txCtx, inviter.ID)
	require.NoError(t, err)
	_, err = repo.EnsureUserAffiliate(txCtx, invitee.ID)
	require.NoError(t, err)

	bound, err := repo.BindInviter(txCtx, invitee.ID, inviter.ID)
	require.NoError(t, err)
	require.True(t, bound, "invitee must bind to inviter")

	applied, err := repo.AccrueQuota(txCtx, inviter.ID, invitee.ID, 3.5, 0, nil)
	require.NoError(t, err)
	require.True(t, applied, "AccrueQuota must report applied=true")

	// Visible inside the outer tx.
	innerQuota := querySingleFloat(t, txCtx, client,
		"SELECT aff_quota::double precision FROM user_affiliates WHERE user_id = $1", inviter.ID)
	require.InDelta(t, 3.5, innerQuota, 1e-9)

	// Check through the outer client that rollback removed the affiliate profiles.
	require.NoError(t, outerTx.Rollback())

	rows, err := integrationEntClient.QueryContext(ctx,
		"SELECT COUNT(*) FROM user_affiliates WHERE user_id IN ($1, $2)",
		inviter.ID, invitee.ID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var postRollbackCount int
	require.NoError(t, rows.Scan(&postRollbackCount))
	require.Equal(t, 0, postRollbackCount,
		"AccrueQuota must propagate the outer tx — found persisted rows after rollback")
}

func TestAffiliateRepository_TransferQuotaToBalance_EmptyQuota(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })

	u := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-empty-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
		Balance:      3.21,
		Concurrency:  5,
	})

	affCode := fmt.Sprintf("AFF%09d", time.Now().UnixNano()%1_000_000_000)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, 0, 0, NOW(), NOW())`, u.ID, affCode)
	require.NoError(t, err)

	transferred, balance, err := repo.TransferQuotaToBalance(txCtx, u.ID)
	require.ErrorIs(t, err, promotion.ErrAffiliateQuotaEmpty)
	require.InDelta(t, 0.0, transferred, 1e-9)
	require.InDelta(t, 0.0, balance, 1e-9)

	persistedBalance := querySingleFloat(t, txCtx, client,
		"SELECT balance::double precision FROM users WHERE id = $1", u.ID)
	require.InDelta(t, 3.21, persistedBalance, 1e-9)
}

// TestAffiliateRepository_AdminCustomCode checks custom code writes, lookup, and reset.
// Reset restores the generated format and clears the custom flag; replaced codes stop matching.
// Uniqueness conflicts have a separate test because a violation aborts the PostgreSQL transaction.
func TestAffiliateRepository_AdminCustomCode(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })

	u := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-custom-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
	})

	original, err := repo.EnsureUserAffiliate(txCtx, u.ID)
	require.NoError(t, err)
	require.False(t, original.AffCodeCustom, "system-generated codes start as non-custom")
	originalCode := original.AffCode

	// Assign a custom code.
	customCode := fmt.Sprintf("VIP%09d", time.Now().UnixNano()%1_000_000_000)
	require.NoError(t, repo.UpdateUserAffCode(txCtx, u.ID, customCode))

	updated, err := repo.EnsureUserAffiliate(txCtx, u.ID)
	require.NoError(t, err)
	require.Equal(t, customCode, updated.AffCode)
	require.True(t, updated.AffCodeCustom)

	// The custom code resolves to the user.
	byCode, err := repo.GetAffiliateByCode(txCtx, customCode)
	require.NoError(t, err)
	require.Equal(t, u.ID, byCode.UserID)

	// The replaced system code stops matching.
	_, err = repo.GetAffiliateByCode(txCtx, originalCode)
	require.ErrorIs(t, err, promotion.ErrAffiliateProfileNotFound)

	// Reset generates a code and clears the custom flag.
	newSysCode, err := repo.ResetUserAffCode(txCtx, u.ID)
	require.NoError(t, err)
	require.NotEqual(t, customCode, newSysCode)

	reset, err := repo.EnsureUserAffiliate(txCtx, u.ID)
	require.NoError(t, err)
	require.Equal(t, newSysCode, reset.AffCode)
	require.False(t, reset.AffCodeCustom)

	// The replaced custom code stops matching.
	_, err = repo.GetAffiliateByCode(txCtx, customCode)
	require.ErrorIs(t, err, promotion.ErrAffiliateProfileNotFound)
}

// TestAffiliateRepository_AdminCustomCode_Conflict checks the duplicate-code uniqueness constraint.
// This case uses its own transaction because PostgreSQL aborts it on the conflict.
func TestAffiliateRepository_AdminCustomCode_Conflict(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })

	taker := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-conflict-taker-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser, Status: billing.StatusActive,
	})
	requester := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-conflict-req-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser, Status: billing.StatusActive,
	})

	takenCode := fmt.Sprintf("HOT%09d", time.Now().UnixNano()%1_000_000_000)
	require.NoError(t, repo.UpdateUserAffCode(txCtx, taker.ID, takenCode))

	// Assigning another user's code returns a conflict.
	err := repo.UpdateUserAffCode(txCtx, requester.ID, takenCode)
	require.ErrorIs(t, err, promotion.ErrAffiliateCodeTaken)
}

// TestAffiliateRepository_AdminRebateRate checks individual and batch overrides, including clearing them
// with NULL.
func TestAffiliateRepository_AdminRebateRate(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })

	u1 := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-rate-%d-a@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
	})
	u2 := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-rate-%d-b@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser,
		Status:       billing.StatusActive,
	})

	// Set a rate override for u1.
	rate := 42.5
	require.NoError(t, repo.SetUserRebateRate(txCtx, u1.ID, &rate))

	got, err := repo.EnsureUserAffiliate(txCtx, u1.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AffRebateRatePercent)
	require.InDelta(t, 42.5, *got.AffRebateRatePercent, 1e-9)

	// Clear the override.
	require.NoError(t, repo.SetUserRebateRate(txCtx, u1.ID, nil))
	cleared, err := repo.EnsureUserAffiliate(txCtx, u1.ID)
	require.NoError(t, err)
	require.Nil(t, cleared.AffRebateRatePercent)

	// Set both users' rates in a batch.
	batchRate := 15.0
	require.NoError(t, repo.BatchSetUserRebateRate(txCtx, []int64{u1.ID, u2.ID}, &batchRate))

	for _, uid := range []int64{u1.ID, u2.ID} {
		v, err := repo.EnsureUserAffiliate(txCtx, uid)
		require.NoError(t, err)
		require.NotNil(t, v.AffRebateRatePercent)
		require.InDelta(t, 15.0, *v.AffRebateRatePercent, 1e-9)
	}

	// Clear both overrides in a batch.
	require.NoError(t, repo.BatchSetUserRebateRate(txCtx, []int64{u1.ID, u2.ID}, nil))
	for _, uid := range []int64{u1.ID, u2.ID} {
		v, err := repo.EnsureUserAffiliate(txCtx, uid)
		require.NoError(t, err)
		require.Nil(t, v.AffRebateRatePercent)
	}
}

// TestAffiliateRepository_ListUsersWithCustomSettings checks inclusion of users with custom codes or rate
// overrides.
func TestAffiliateRepository_ListUsersWithCustomSettings(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()

	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })

	// Users with neither override are excluded.
	plainEmail := fmt.Sprintf("affiliate-plain-%d@example.com", time.Now().UnixNano())
	uPlain := mustCreateUser(t, client, &identity.User{
		Email: plainEmail, PasswordHash: "hash",
		Role: identity.RoleUser, Status: billing.StatusActive,
	})
	_, err := repo.EnsureUserAffiliate(txCtx, uPlain.ID)
	require.NoError(t, err)

	// A custom invitation code qualifies the user for the list.
	uCode := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-codeonly-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser, Status: billing.StatusActive,
	})
	require.NoError(t, repo.UpdateUserAffCode(txCtx, uCode.ID, fmt.Sprintf("VIP%09d", time.Now().UnixNano()%1_000_000_000)))

	// A custom rebate rate also qualifies the user for the list.
	uRate := mustCreateUser(t, client, &identity.User{
		Email:        fmt.Sprintf("affiliate-rateonly-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         identity.RoleUser, Status: billing.StatusActive,
	})
	r := 33.3
	require.NoError(t, repo.SetUserRebateRate(txCtx, uRate.ID, &r))

	entries, total, err := repo.ListUsersWithCustomSettings(txCtx, promotion.AffiliateAdminFilter{
		Page: 1, PageSize: 100,
	})
	require.NoError(t, err)

	// Index results by user ID to check the three fixtures created by this test.
	byUserID := make(map[int64]promotion.AffiliateAdminEntry, len(entries))
	for _, e := range entries {
		byUserID[e.UserID] = e
	}

	require.NotContains(t, byUserID, uPlain.ID, "users without overrides must not appear")

	codeEntry, ok := byUserID[uCode.ID]
	require.True(t, ok, "custom-code user missing from list")
	require.True(t, codeEntry.AffCodeCustom)
	require.Nil(t, codeEntry.AffRebateRatePercent)

	rateEntry, ok := byUserID[uRate.ID]
	require.True(t, ok, "custom-rate user missing from list")
	require.False(t, rateEntry.AffCodeCustom)
	require.NotNil(t, rateEntry.AffRebateRatePercent)
	require.InDelta(t, 33.3, *rateEntry.AffRebateRatePercent, 1e-9)

	require.GreaterOrEqual(t, total, int64(2), "total must include at least our 2 custom rows")
}

// testEntTx starts an Ent transaction and rolls it back during cleanup.
func testEntTx(t *testing.T) *dbent.Tx {
	t.Helper()
	client := testEntClient(t)
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// mustCreateUser persists a user fixture and fills its generated identity and timestamps.
func mustCreateUser(t *testing.T, client *dbent.Client, u *identity.User) *identity.User {
	t.Helper()
	ctx := context.Background()

	if u.Email == "" {
		u.Email = "user-" + time.Now().Format(time.RFC3339Nano) + "@example.com"
	}
	if u.PasswordHash == "" {
		u.PasswordHash = "test-password-hash"
	}
	if u.Role == "" {
		u.Role = identity.RoleUser
	}
	if u.Status == "" {
		u.Status = billing.StatusActive
	}
	if u.Concurrency == 0 {
		u.Concurrency = 5
	}

	create := client.User.Create().
		SetEmail(u.Email).
		SetPasswordHash(u.PasswordHash).
		SetRole(u.Role).
		SetStatus(u.Status).
		SetBalance(u.Balance).
		SetConcurrency(u.Concurrency).
		SetUsername(u.Username).
		SetNotes(u.Notes)
	if !u.CreatedAt.IsZero() {
		create.SetCreatedAt(u.CreatedAt)
	}
	if !u.UpdatedAt.IsZero() {
		create.SetUpdatedAt(u.UpdatedAt)
	}

	created, err := create.Save(ctx)
	require.NoError(t, err, "create user")

	u.ID = created.ID
	u.CreatedAt = created.CreatedAt
	u.UpdatedAt = created.UpdatedAt

	if len(u.AllowedGroups) > 0 {
		for _, groupID := range u.AllowedGroups {
			_, err := client.UserAllowedGroup.Create().
				SetUserID(u.ID).
				SetGroupID(groupID).
				Save(ctx)
			require.NoError(t, err, "create user_allowed_groups row")
		}
	}

	return u
}
