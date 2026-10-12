package migrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestRemoveOpenAIOAuthImportDefaultsMigration executes cleanup against a SQLite
// fixture containing OAuth import defaults and cooldown configuration. Two
// executions must remove the import template while retaining the exact saved
// cooldown value.
func TestRemoveOpenAIOAuthImportDefaultsMigration(t *testing.T) {
	content, err := FS.ReadFile("291_remove_openai_oauth_import_defaults.sql")
	require.NoError(t, err)
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	// One live SQLite connection keeps schema setup, deletion, and assertions
	// inside the same :memory: database throughout this migration replay fixture.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES
		('openai_oauth_import_defaults', '{"provider":{"concurrency":7}}'),
		('openai_403_cooldown', '{"enabled":true}')`)
	require.NoError(t, err)

	// A repeated upgrade can encounter an already absent import template. Run
	// cleanup twice to exercise that state before inspecting the remaining setting.
	for range 2 {
		_, err = db.Exec(string(content))
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'openai_oauth_import_defaults'`).Scan(&count))
	require.Zero(t, count)
	var value string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key = 'openai_403_cooldown'`).Scan(&value))
	require.Equal(t, `{"enabled":true}`, value)
}
