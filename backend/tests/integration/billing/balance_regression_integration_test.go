//go:build integration

package billing_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/postgres"
)

// Administrator balance replacement can wait behind a settlement update that
// has already locked the user row. The fixture subtracts twenty inside an open
// PostgreSQL transaction and starts SetBalance on a separate connection,
// leaving that operation blocked until the transaction commits. The requested
// replacement is two hundred, so the expected old value is the committed
// eighty.
//
// A buffered outcome channel collects the setter's result after the blocking
// transaction is released. The polling assertion looks for a matching UPDATE
// users AS u statement waiting on a database lock before committing. The final
// assertions check the returned old and new balances, making the old-value
// observation sensitive to settlement completion.

// TestSetBalanceReturnsLockedOldValue blocks balance replacement behind a
// PostgreSQL transaction that subtracts twenty. After that transaction commits,
// SetBalance reports old balance eighty and new balance two hundred.
func TestSetBalanceReturnsLockedOldValue(t *testing.T) {
	ctx := context.Background()
	client := committedEntitlementClient(t)
	user := mustCreateUser(t, client, &identity.User{Email: fmt.Sprintf("test-set-%d@example.com", time.Now().UnixNano()), Balance: 100})
	repo := postgres.NewUserStore(client, integrationDB)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	// Retain rollback cleanup while the competing setter waits; an early
	// assertion failure releases the row lock even when the normal commit
	// is never reached.
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "UPDATE users SET balance = balance - 20 WHERE id = $1", user.ID)
	require.NoError(t, err)
	// outcome carries the completed balance replacement result from the
	// competing connection.
	type outcome struct {
		change identity.BalanceChange // returned old and new balance values
		err    error                  // competing setter operation error
	}
	// Buffer the setter result so it can finish after lock release even
	// before the test receives the value used by its final balance
	// assertions.
	done := make(chan outcome, 1)
	go func() { c, e := repo.SetBalance(ctx, user.ID, 200); done <- outcome{c, e} }()
	// Observe a matching lock-waiting replacement query before releasing
	// settlement's row lock. Polling bounds how long this fixture waits for
	// that database state.
	require.Eventually(t, func() bool {
		var waiting int
		err := integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE query LIKE '%UPDATE users AS u%' AND wait_event_type = 'Lock' AND pid <> pg_backend_pid()").Scan(&waiting)
		return err == nil && waiting > 0
	}, 5*time.Second, 10*time.Millisecond, "set 已读取旧快照并阻塞在正在结算的用户行")
	// Publish the settlement subtraction before receiving the replacement
	// result; the setter must derive its returned old balance from the
	// released row state.
	require.NoError(t, tx.Commit())
	got := <-done
	require.NoError(t, got.err)
	require.Equal(t, float64(80), got.change.Old, "审计增量必须依据锁解除后的真实旧余额")
	require.Equal(t, float64(200), got.change.New)
}
