//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// TestHistoricalIngressCleanupDryRunAndExecute commits local-key, quota,
// other-phase, and newer fixture rows before scanning one-row pages. It checks
// dry-run classifications retain all rows, then execution removes only the two
// matched older auth fixtures.
func TestHistoricalIngressCleanupDryRunAndExecute(t *testing.T) {
	ctx := context.Background()
	// Clear the shared error-log fixture table after this test so dry-run and
	// execution cases do not leave their committed rows behind.
	t.Cleanup(func() { _, err := integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs"); require.NoError(t, err) })
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	// Attempt rollback on any early assertion exit; the committed fixture remains
	// available for subsequent repository connections after this defer becomes
	// harmless.
	defer func() { _ = tx.Rollback() }()
	// Truncate and insert all fixture rows inside this transaction, then commit
	// before repository scanning can read them through other pooled connections.
	_, err = tx.ExecContext(ctx, "TRUNCATE ops_error_logs")
	require.NoError(t, err)
	before := time.Now().UTC()
	for _, v := range []struct {
		// code: embedded rejection identity. phase: stored error phase.
		code, phase string
		at          time.Time // stored fixture creation instant
	}{{"INVALID_API_KEY", "auth", before.Add(-time.Minute)}, {"API_KEY_REQUIRED", "auth", before.Add(-time.Minute)}, {"API_KEY_QUOTA_EXHAUSTED", "auth", before.Add(-time.Minute)}, {"INVALID_API_KEY", "upstream", before.Add(-time.Minute)}, {"INVALID_API_KEY", "auth", before.Add(time.Minute)}} {
		_, err = tx.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,severity,status_code,error_message,error_body,created_at) VALUES($1,'authentication_error','warn',401,'',$2,$3)`, v.phase, `{"code":"`+v.code+`"}`, v.at)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	store := NewHistoricalIngressCleanup(integrationDB)
	counts, scanned, matched, deleted, err := ops.CleanupHistoricalIngress(ctx, store, before, 1, false)
	require.NoError(t, err)
	require.EqualValues(t, 3, scanned)
	require.EqualValues(t, 2, matched)
	require.Zero(t, deleted)
	require.Equal(t, map[string]int64{"invalid_key": 1, "missing_key": 1}, counts)
	var actual int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_error_logs").Scan(&actual))
	require.Equal(t, 5, actual)
	_, scanned, matched, deleted, err = ops.CleanupHistoricalIngress(ctx, store, before, 1, true)
	require.NoError(t, err)
	require.EqualValues(t, 3, scanned)
	require.EqualValues(t, 2, matched)
	require.EqualValues(t, 2, deleted)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM ops_error_logs").Scan(&actual))
	require.Equal(t, 3, actual)
}
