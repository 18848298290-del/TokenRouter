package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroupFreeOpenAIFastMigration checks the free Fast field's type, default, and column description.
func TestGroupFreeOpenAIFastMigration(t *testing.T) {
	content, err := FS.ReadFile("264_group_free_openai_fast.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS free_openai_fast BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "COMMENT ON COLUMN groups.free_openai_fast")
}
