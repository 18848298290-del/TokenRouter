package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupFreeOpenAIFastMigration inspects the free-Fast group setting SQL.
// It requires a non-null false default and a COMMENT ON COLUMN declaration,
// keeping the opt-in field and its database documentation in the migration.
func TestGroupFreeOpenAIFastMigration(t *testing.T) {
	content, err := FS.ReadFile("264_group_free_openai_fast.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS free_openai_fast BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.free_openai_fast")
}
