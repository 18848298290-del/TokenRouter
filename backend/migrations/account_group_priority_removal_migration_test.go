package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAccountGroupPriorityRemovalMigration inspects the embedded priority-retirement SQL.
// It requires removal statements for the priority column and all three named
// indexes, protecting the migration's complete cleanup of that stored setting.
func TestAccountGroupPriorityRemovalMigration(t *testing.T) {
	content, err := FS.ReadFile("240_remove_account_group_priority.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "drop index if exists idx_account_groups_priority")
	require.Contains(t, sql, "drop index if exists idx_account_groups_group_priority_account")
	require.Contains(t, sql, "drop index if exists idx_account_groups_account_priority_group")
	require.Contains(t, sql, "drop column if exists priority")
}
