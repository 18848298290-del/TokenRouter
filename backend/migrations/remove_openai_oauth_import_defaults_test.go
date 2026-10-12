package migrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestRemoveOpenAIOAuthImportDefaultsMigration checks template deletion, replay, and retention of other
// settings.
func TestRemoveOpenAIOAuthImportDefaultsMigration(t *testing.T) {
	content, err := FS.ReadFile("291_remove_openai_oauth_import_defaults.sql")
	require.NoError(t, err)
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES
		('openai_oauth_import_defaults', '{"provider":{"concurrency":7}}'),
		('openai_403_cooldown', '{"enabled":true}')`)
	require.NoError(t, err)

	// Configuration cleanup must tolerate replay.
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
