package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserEmailAliasDedupIndexMigration inspects the concurrent dot-stripped email
// index SQL. It requires the normalization expression and active-row predicate,
// protecting the lookup index used for duplicate mailbox detection.
func TestUserEmailAliasDedupIndexMigration(t *testing.T) {
	content, err := FS.ReadFile("252_users_email_alias_dedup_index_notx.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	require.Contains(t, sql, "create index concurrently if not exists idx_users_email_dot_stripped")
	require.Contains(t, sql, "replace(lower(trim(email)), '.', '')")
	require.Contains(t, sql, "where deleted_at is null")
}
