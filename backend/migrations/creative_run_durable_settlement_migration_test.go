package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunDurableSettlementMigration inspects the recovery and settlement
// schema additions. It requires seven run columns, the unique run-operation
// outbox declaration, and the named managed-key index in the embedded SQL.
func TestCreativeRunDurableSettlementMigration(t *testing.T) {
	content, err := FS.ReadFile("257_creative_run_durable_settlement.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	for _, column := range []string{"provisioning_phase", "provider_result_recorded_at", "settlement_attempt_count", "release_attempt_count", "next_reconcile_at", "last_reconcile_error", "release_target_status"} {
		require.Contains(t, sql, "add column if not exists "+column)
	}
	require.Contains(t, sql, "create table if not exists creative_run_outbox")
	require.Contains(t, sql, "unique (run_id, operation)")
	require.Contains(t, sql, "api_keys_creative_managed_user_group_uq")
}
