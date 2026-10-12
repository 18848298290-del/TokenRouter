package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration203DefaultsOrdinaryOpenAIAndInheritsForSparkShadows inspects the
// historical long-context setting SQL. It requires ordinary-parent selection,
// Spark-shadow handling, parent extras, and JSON type checks around the setting.
func TestMigration203DefaultsOrdinaryOpenAIAndInheritsForSparkShadows(t *testing.T) {
	content, err := FS.ReadFile("203_default_openai_long_context_billing.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "parent_account_id IS NULL")
	require.Contains(t, sql, "quota_dimension = 'spark'")
	require.Contains(t, sql, "parent.extra")
	require.Contains(t, sql, "jsonb_typeof")
	require.Contains(t, sql, "openai_long_context_billing_enabled")
}

// TestMigration203GuardsMixedVersionAccountWrites inspects the historical trigger
// and notification SQL. It requires write-trigger declarations, boolean checks,
// account-change outbox text, shadow propagation, and access to the previous
// setting value during updates.
func TestMigration203GuardsMixedVersionAccountWrites(t *testing.T) {
	content, err := FS.ReadFile("203_default_openai_long_context_billing.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "RETURNS TRIGGER")
	require.Contains(t, sql, "BEFORE INSERT OR UPDATE")
	require.Contains(t, sql, "CREATE TRIGGER")
	require.Contains(t, sql, "must be a boolean")
	require.Contains(t, sql, "INSERT INTO scheduler_outbox")
	require.Contains(t, sql, "'account_changed'")
	require.Contains(t, sql, "jsonb_typeof(extra->'openai_long_context_billing_enabled') IS DISTINCT FROM 'boolean'")
	require.Contains(t, sql, "WITH shadow_values AS")
	require.Contains(t, sql, "TG_OP = 'UPDATE'")
	require.Contains(t, sql, "OLD.extra->'openai_long_context_billing_enabled'")
}
