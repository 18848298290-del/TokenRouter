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

// Affiliate transfers report the quota claimed before clearing it and retain
// ledger observations of the resulting wallet and quota values. PostgreSQL
// transaction cases inspect that transfer, outer-transaction accrual rollback,
// empty available quota, custom invitation-code ownership, nullable rebate
// overrides, and administrator lists of users with overrides.
//
// User fixture construction saves the owning row before inserting its
// allowed-group references, leaving transaction completion to the supplied
// client owner. Deliberate duplicate-code errors run in their own rollback
// transaction because PostgreSQL aborts that transaction after the uniqueness
// violation. Scalar query helpers inspect their first row and discard
// result-close errors, while fixture transaction cleanup discards errors from
// already completed rollback.

// querySingleFloat returns the first row's floating-point value and fails the
// test on query, missing-row, scan, or row errors reported after that scan.
// Additional rows remain unread and deferred close errors are discarded, so
// callers supply a scalar query.
func querySingleFloat(t *testing.T, ctx context.Context, client *dbent.Client, query string, args ...any) float64 {
	t.Helper()
	rows, err := client.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	// Close each scalar or ledger result before returning to the borrowed
	// client; the fixture discards close errors while query and scan errors
	// remain assertion failures.
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next(), "expected one row")
	var value float64
	require.NoError(t, rows.Scan(&value))
	require.NoError(t, rows.Err())
	return value
}

// querySingleInt returns the first row's integer value and fails the test on
// query, missing-row, scan, or currently reported row errors. Additional rows
// remain unread and deferred close errors are discarded while the caller owns
// the executor.
func querySingleInt(t *testing.T, ctx context.Context, client *dbent.Client, query string, args ...any) int {
	t.Helper()
	rows, err := client.QueryContext(ctx, query, args...)
	require.NoError(t, err)
	// Close each scalar or ledger result before returning to the borrowed
	// client; the fixture discards close errors while query and scan errors
	// remain assertion failures.
	defer func() { _ = rows.Close() }()

	require.True(t, rows.Next(), "expected one row")
	var value int
	require.NoError(t, rows.Scan(&value))
	require.NoError(t, rows.Err())
	return value
}

// TestAffiliateRepository_TransferQuotaToBalance_UsesClaimedQuotaBeforeClear
// transfers 12.34 from an affiliate quota into a 5.5 wallet. Returned amounts,
// cleared quota, stored balance, and one ledger record all reflect the claimed
// pre-clear amount.
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

	// Nine timestamp digits keep this prefixed fixture code within the
	// invitation length limit.
	affCode := fmt.Sprintf("AFF%09d", time.Now().UnixNano()%1_000_000_000)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, $3, $3, NOW(), NOW())`, u.ID, affCode, 12.34)
	require.NoError(t, err)

	transferred, balance, err := repo.TransferQuotaToBalance(txCtx, u.ID)
	require.NoError(t, err)
	// Compare claimed funds and ledger values with the same small numeric
	// tolerance.
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
	// Close each scalar or ledger result before returning to the borrowed
	// client; the fixture discards close errors while query and scan errors
	// remain assertion failures.
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

// TestAffiliateRepository_AccrueQuota_ReusesOuterTransaction creates affiliate
// profiles and an accrual inside a context-bound outer transaction. The credit
// is visible inside that transaction, and rollback leaves no matching affiliate
// profiles outside it.
func TestAffiliateRepository_AccrueQuota_ReusesOuterTransaction(t *testing.T) {
	ctx := context.Background()

	integrationEntClient, _ := testStore(t)
	outerTx, err := integrationEntClient.Tx(ctx)
	require.NoError(t, err, "begin outer tx")
	// Retain outer rollback cleanup even when an assertion exits before the
	// explicit rollback, releasing fixture writes and tolerating an already
	// completed transaction.
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
	// A small numeric tolerance checks the uncommitted accrued quota value.
	require.InDelta(t, 3.5, innerQuota, 1e-9)

	// Roll back the caller transaction before inspecting profiles through
	// its base client so that query exposes any accrual participant that
	// committed fixture work early.
	require.NoError(t, outerTx.Rollback())

	rows, err := integrationEntClient.QueryContext(ctx,
		"SELECT COUNT(*) FROM user_affiliates WHERE user_id IN ($1, $2)",
		inviter.ID, invitee.ID)
	require.NoError(t, err)
	// Close each scalar or ledger result before returning to the borrowed
	// client; the fixture discards close errors while query and scan errors
	// remain assertion failures.
	defer func() { _ = rows.Close() }()
	require.True(t, rows.Next())
	var postRollbackCount int
	require.NoError(t, rows.Scan(&postRollbackCount))
	require.Equal(t, 0, postRollbackCount,
		"AccrueQuota must propagate the outer tx — found persisted rows after rollback")
}

// TestAffiliateRepository_TransferQuotaToBalance_EmptyQuota supplies an
// affiliate profile with zero available quota. Transfer returns the empty-quota
// error with zero result amounts, while the stored wallet retains 3.21.
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

	// Nine timestamp digits keep this prefixed fixture code within the
	// invitation length limit.
	affCode := fmt.Sprintf("AFF%09d", time.Now().UnixNano()%1_000_000_000)
	_, err := client.ExecContext(txCtx, `
INSERT INTO user_affiliates (user_id, aff_code, aff_quota, aff_history_quota, created_at, updated_at)
VALUES ($1, $2, 0, 0, NOW(), NOW())`, u.ID, affCode)
	require.NoError(t, err)

	transferred, balance, err := repo.TransferQuotaToBalance(txCtx, u.ID)
	require.ErrorIs(t, err, promotion.ErrAffiliateQuotaEmpty)
	// Numeric tolerance compares empty result amounts with the unchanged
	// wallet read.
	require.InDelta(t, 0.0, transferred, 1e-9)
	require.InDelta(t, 0.0, balance, 1e-9)

	persistedBalance := querySingleFloat(t, txCtx, client,
		"SELECT balance::double precision FROM users WHERE id = $1", u.ID)
	require.InDelta(t, 3.21, persistedBalance, 1e-9)
}

// TestAffiliateRepository_AdminCustomCode assigns a custom invitation code and
// then resets it to a generated code. Stored custom flags and owner lookup
// follow each selected value, and replaced codes stop matching.
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

	// Assign a custom code. Nine timestamp digits keep this prefixed
	// fixture code within the invitation length limit.
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

// TestAffiliateRepository_AdminCustomCode_Conflict assigns one user's code to
// another user in the same case transaction with registered rollback cleanup.
// The duplicate request returns AffiliateCodeTaken and is the last operation
// before cleanup rolls that transaction back.
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

	// Nine timestamp digits keep this prefixed fixture code within the
	// invitation length limit.
	takenCode := fmt.Sprintf("HOT%09d", time.Now().UnixNano()%1_000_000_000)
	require.NoError(t, repo.UpdateUserAffCode(txCtx, taker.ID, takenCode))

	// Perform the duplicate assignment last because its PostgreSQL
	// uniqueness error aborts this case transaction, leaving remaining
	// fixture cleanup to the registered rollback.
	err := repo.UpdateUserAffCode(txCtx, requester.ID, takenCode)
	require.ErrorIs(t, err, promotion.ErrAffiliateCodeTaken)
}

// TestAffiliateRepository_AdminRebateRate sets and clears individual and batch
// rebate-rate overrides. Stored nullable pointers reflect each selection,
// including nil after clearing both user overrides in the final batch.
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
	// A small numeric tolerance checks individual and batch percentage
	// overrides.
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

// TestAffiliateRepository_ListUsersWithCustomSettings creates plain,
// custom-code, and custom-rate profiles. The result excludes the plain fixture,
// reports the two configured overrides, and has total count at least two.
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
	// Nine timestamp digits keep this prefixed fixture code within the
	// invitation length limit.
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

	// Index results by user ID to check the three fixtures created by this
	// test.
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
	// Compare the returned percentage with the configured override using a
	// small tolerance.
	require.InDelta(t, 33.3, *rateEntry.AffRebateRatePercent, 1e-9)

	require.GreaterOrEqual(t, total, int64(2), "total must include at least our 2 custom rows")
}

// testEntTx opens a transaction on a newly cloned promotion database and
// registers rollback after case work ends. Callers own any earlier completion;
// cleanup discards errors from an already ended transaction.
func testEntTx(t *testing.T) *dbent.Tx {
	t.Helper()
	client := testEntClient(t)
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	// Roll back before Ent and clone cleanup run so case-owned writes and
	// locks are released while their executor still exists; repeated
	// completion errors are discarded.
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// mustCreateUser persists selected identity fields and fills the supplied
// object's generated ID and timestamps. Empty email, password, role, and status
// receive fixture values, and zero concurrency becomes five.
//
// Allowed-group rows are written after user creation so they can reference the
// stored owner ID. All writes use the supplied client and a background context;
// a later grant failure can follow earlier writes before the test fails. The
// caller controls rollback or committed-fixture cleanup for that selected
// storage scope.
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

	// Insert grants after the user ID is stored so every reference has its
	// owner; the caller's supplied client determines rollback of partial
	// fixture creation after failure.
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
