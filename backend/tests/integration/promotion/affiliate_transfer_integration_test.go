//go:build integration

package promotion_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	promotionpostgres "github.com/TokenFlux/TokenRouter/internal/promotion/postgres"
)

// Affiliate quota transfer clears available quota, credits the wallet, and
// writes its ledger record in one PostgreSQL transaction. The injected ledger
// trigger reads the updated wallet and cleared quota before rejecting the
// insert, so the failure also checks that billing credit participates in the
// transfer transaction.
//
// The fixture starts with ten available quota, balance three, and cumulative
// recharge seven. Its trigger expects balance thirteen and zero available
// quota; outside reads after rejection expect the starting values and no
// transfer ledger row. Trigger removal is registered after function creation
// and before trigger creation, allowing partial setup to be cleaned before the
// independent database clone closes.

// TestAffiliateTransferLedgerFailureRollsBackFunds rejects the transfer ledger
// after verifying transaction-local credit and quota clearing. The wallet,
// cumulative recharge, and available quota retain their preceding values, with
// no transfer ledger row.
func TestAffiliateTransferLedgerFailureRollsBackFunds(t *testing.T) {
	ctx := context.Background()
	client, integrationDB := testStore(t)
	user, err := client.User.Create().SetEmail("test-transfer@example.com").SetPasswordHash("hash").SetBalance(3).SetTotalRecharged(7).Save(ctx)
	require.NoError(t, err)
	repo := promotionpostgres.NewAffiliateRepository(client, func(tx *dbent.Tx) promotionpostgres.TransferBalance { return billingpostgres.BalanceInTx(tx) })
	_, err = repo.EnsureUserAffiliate(ctx, user.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE user_affiliates SET aff_quota=10, aff_history_quota=10 WHERE user_id=$1", user.ID)
	require.NoError(t, err)
	name := fmt.Sprintf("test_transfer_failure_%d", user.ID)
	// Check credited balance and cleared quota in the ledger trigger before
	// rejecting its insert; outside reads then expose earlier fund effects
	// that escaped rollback.
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.user_id=%d AND NEW.action='transfer' THEN IF (SELECT balance FROM users WHERE id=%d)<>13 OR (SELECT aff_quota FROM user_affiliates WHERE user_id=%d)<>0 THEN RAISE EXCEPTION 'transfer was not visible in same transaction'; END IF; RAISE EXCEPTION 'test forced ledger failure'; END IF; RETURN NEW; END; $$`, name, user.ID, user.ID, user.ID))
	require.NoError(t, err)
	// Remove the failure trigger before its function after the rollback
	// assertions; IF EXISTS also permits cleanup if setup exits after
	// acquiring only part of the fixture.
	t.Cleanup(func() {
		_, e := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON user_affiliate_ledger; DROP FUNCTION IF EXISTS %s()", name, name))
		require.NoError(t, e)
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON user_affiliate_ledger FOR EACH ROW EXECUTE FUNCTION %s()", name, name))
	require.NoError(t, err)
	_, _, err = repo.TransferQuotaToBalance(ctx, user.ID)
	require.ErrorContains(t, err, "test forced ledger failure")
	current, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 3.0, current.Balance)
	require.Equal(t, 7.0, current.TotalRecharged)
	var quota float64
	var records int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT aff_quota FROM user_affiliates WHERE user_id=$1", user.ID).Scan(&quota))
	require.Equal(t, 10.0, quota)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id=$1 AND action='transfer'", user.ID).Scan(&records))
	require.Zero(t, records)
}
