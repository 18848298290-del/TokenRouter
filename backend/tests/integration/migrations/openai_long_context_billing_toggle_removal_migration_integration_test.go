//go:build integration

package migrations_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
)

// Retiring the account billing toggle removes both its metadata key and the
// functions and triggers that can recreate or propagate it. A historical
// accounts schema is first given those mechanisms again, then OpenAI and
// Anthropic fixtures check retained nested metadata after two removal
// executions.

// TestMigration241RemovesOpenAILongContextBillingToggleIdempotently reinstalls
// migration 203 and writes toggle-bearing accounts on two platforms. It checks
// replay, retained custom JSON, and absence of the retired triggers and public
// functions.
func TestMigration241RemovesOpenAILongContextBillingToggleIdempotently(t *testing.T) {
	tx := historicalTx(t, "282_")
	ctx := context.Background()
	legacySQL, err := dbmigrations.FS.ReadFile("203_default_openai_long_context_billing.sql")
	require.NoError(t, err)
	removalSQL, err := dbmigrations.FS.ReadFile("241_remove_openai_long_context_billing_toggle.sql")
	require.NoError(t, err)

	// The pre-282 history has already run migration 241. Reinstall
	// migration 203 so removal encounters the functions and triggers as
	// well as legacy metadata.
	_, err = tx.ExecContext(ctx, string(legacySQL))
	require.NoError(t, err)

	insertAccount := func(name, platform, extra string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ($1, $2, 'oauth', $3::jsonb)
RETURNING id
`, name, platform, extra).Scan(&id))
		return id
	}

	openAIID := insertAccount(
		"migration-241-openai",
		"openai",
		`{"openai_long_context_billing_enabled":true,"custom":{"nested":1}}`,
	)
	otherPlatformID := insertAccount(
		"migration-241-anthropic",
		"anthropic",
		`{"openai_long_context_billing_enabled":"legacy","custom":{"nested":2}}`,
	)

	// The second removal runs after both mechanism and metadata cleanup,
	// checking that missing triggers, functions, and keys permit the same
	// SQL to complete again.
	_, err = tx.ExecContext(ctx, string(removalSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(removalSQL))
	require.NoError(t, err)

	assertExtra := func(accountID int64, nested float64) {
		t.Helper()
		var rawExtra []byte
		require.NoError(t, tx.QueryRowContext(ctx, "SELECT extra FROM accounts WHERE id = $1", accountID).Scan(&rawExtra))
		var extra map[string]any
		require.NoError(t, json.Unmarshal(rawExtra, &extra))
		require.NotContains(t, extra, "openai_long_context_billing_enabled")
		require.Equal(t, map[string]any{"nested": nested}, extra["custom"])
	}
	assertExtra(openAIID, 1)
	assertExtra(otherPlatformID, 2)

	var triggerCount int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_trigger
WHERE tgname IN (
    'accounts_enforce_openai_long_context_billing_extra',
    'accounts_propagate_openai_long_context_billing_extra'
)
`).Scan(&triggerCount))
	require.Zero(t, triggerCount)

	var functionCount int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_proc AS procedure
JOIN pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
WHERE namespace.nspname = 'public'
  AND procedure.proname IN (
      'enforce_openai_long_context_billing_extra',
      'propagate_openai_long_context_billing_extra_to_shadows'
  )
`).Scan(&functionCount))
	require.Zero(t, functionCount)
}
