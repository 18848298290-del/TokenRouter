//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
)

// Historical OpenAI account metadata contains a billing toggle inherited by
// Spark shadows, with scheduler events emitted when a shadow value changes. The
// pre-282 database retains the accounts name but has already removed the toggle
// triggers, so the fixture reinstalls migration 203 to test backfill and
// subsequent parent and legacy-writer updates.

// TestMigration203EnforcesOpenAILongContextBillingWriteInvariant restores the
// historical toggle migration over missing, boolean, and malformed account
// values. It checks default-off backfill, inherited shadow updates, selected
// outbox counts, legacy replacement writes, and rejection of a new nonboolean
// value.
func TestMigration203EnforcesOpenAILongContextBillingWriteInvariant(t *testing.T) {
	tx := historicalTx(t, "282_")
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("203_default_openai_long_context_billing.sql")
	require.NoError(t, err)
	// Remove any toggle triggers before inserting malformed legacy values.
	// The pre-282 cutoff retains accounts, and replaying migration 203 then
	// supplies the write-time enforcement.
	_, err = tx.ExecContext(ctx, `
DROP TRIGGER IF EXISTS accounts_propagate_openai_long_context_billing_extra ON accounts;
DROP TRIGGER IF EXISTS accounts_enforce_openai_long_context_billing_extra ON accounts;
`)
	require.NoError(t, err)

	var ordinaryID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-203-ordinary', 'openai', 'oauth', '{}'::jsonb)
RETURNING id
`).Scan(&ordinaryID))

	var parentID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-203-parent', 'openai', 'oauth', '{"openai_long_context_billing_enabled":false}'::jsonb)
RETURNING id
`).Scan(&parentID))

	var shadowID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra, parent_account_id, quota_dimension)
VALUES ('migration-203-shadow', 'openai', 'oauth', '{}'::jsonb, $1, 'spark')
RETURNING id
`, parentID).Scan(&shadowID))

	var malformedLegacyID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-203-malformed-legacy', 'openai', 'oauth', '{"openai_long_context_billing_enabled":"false"}'::jsonb)
RETURNING id
`).Scan(&malformedLegacyID))

	// The first pass normalizes historical values and creates the triggers.
	// A second pass exercises replay before querying backfilled values and
	// the shadow's event count.
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var ordinaryEnabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'openai_long_context_billing_enabled')::boolean
FROM accounts
WHERE id = $1
`, ordinaryID).Scan(&ordinaryEnabled))
	require.False(t, ordinaryEnabled)

	var shadowEnabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'openai_long_context_billing_enabled')::boolean
FROM accounts
WHERE id = $1
`, shadowID).Scan(&shadowEnabled))
	require.False(t, shadowEnabled)

	var initialShadowOutboxEvents int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM scheduler_outbox
WHERE event_type = 'account_changed' AND account_id = $1
`, shadowID).Scan(&initialShadowOutboxEvents))
	require.Equal(t, 1, initialShadowOutboxEvents)

	var malformedLegacyEnabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'openai_long_context_billing_enabled')::boolean
FROM accounts
WHERE id = $1
`, malformedLegacyID).Scan(&malformedLegacyEnabled))
	require.False(t, malformedLegacyEnabled)
	_, err = tx.ExecContext(ctx, `
UPDATE accounts
SET extra = extra || '{"migration_203_unrelated_update":true}'::jsonb
WHERE id = $1
`, malformedLegacyID)
	require.NoError(t, err)

	// Clear setup events before replacing the parent's extra with a legacy
	// writer payload; the following count can then detect an unnecessary
	// shadow notification.
	_, err = tx.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
UPDATE accounts
SET extra = '{"legacy_writer_replaced_extra":true}'::jsonb
WHERE id = $1
`, parentID)
	require.NoError(t, err)
	var parentEnabled bool
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'openai_long_context_billing_enabled')::boolean
FROM accounts
WHERE id = $1
`, parentID).Scan(&parentEnabled))
	require.False(t, parentEnabled)
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'openai_long_context_billing_enabled')::boolean
FROM accounts
WHERE id = $1
`, shadowID).Scan(&shadowEnabled))
	require.False(t, shadowEnabled)
	var preservedOptOutEvents int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM scheduler_outbox
WHERE event_type = 'account_changed' AND account_id = $1
`, shadowID).Scan(&preservedOptOutEvents))
	require.Zero(t, preservedOptOutEvents)

	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-203-rolling-writer', 'openai', 'oauth', '{}'::jsonb)
RETURNING (extra->>'openai_long_context_billing_enabled')::boolean
`).Scan(&ordinaryEnabled))
	require.False(t, ordinaryEnabled)

	_, err = tx.ExecContext(ctx, "TRUNCATE scheduler_outbox")
	require.NoError(t, err)
	// Enable the parent's toggle after clearing earlier events. The shadow
	// value and one matching outbox row check propagation for this single
	// effective-value change.
	_, err = tx.ExecContext(ctx, `
UPDATE accounts
SET extra = jsonb_set(extra, '{openai_long_context_billing_enabled}', 'true'::jsonb, true)
WHERE id = $1
`, parentID)
	require.NoError(t, err)
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'openai_long_context_billing_enabled')::boolean
FROM accounts
WHERE id = $1
`, shadowID).Scan(&shadowEnabled))
	require.True(t, shadowEnabled)

	var shadowOutboxEvents int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM scheduler_outbox
WHERE event_type = 'account_changed' AND account_id = $1
`, shadowID).Scan(&shadowOutboxEvents))
	require.Equal(t, 1, shadowOutboxEvents)

	// This final write intentionally aborts the fixture transaction. Assert
	// the trigger diagnostic, then let test cleanup roll back the completed
	// setup and rejected write.
	_, err = tx.ExecContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-203-malformed', 'openai', 'oauth', '{"openai_long_context_billing_enabled":"false"}'::jsonb)
`)
	require.ErrorContains(t, err, "openai_long_context_billing_enabled must be a boolean")
}
