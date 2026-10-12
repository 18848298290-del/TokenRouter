package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupForceOpenAIFastMigration checks the group Fast switch's type, default, and column description.
func TestGroupForceOpenAIFastMigration(t *testing.T) {
	content, err := FS.ReadFile("262_group_force_openai_fast.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS force_openai_fast BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.force_openai_fast")
	require.Contains(t, sql, "全局 Fast/Flex")
}
