package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBatchImageInternalModelMigration inspects the internal-model snapshot migration.
// It requires a non-null empty-default column and the absence of job update
// statements, protecting historical jobs from an inferred internal-model backfill.
func TestBatchImageInternalModelMigration(t *testing.T) {
	content, err := FS.ReadFile("233_add_batch_image_internal_model.sql")
	require.NoError(t, err)

	sql := strings.ToLower(strings.Join(strings.Fields(string(content)), " "))
	require.Contains(t, sql, "add column if not exists internal_model varchar(128) not null default ''")
	require.NotContains(t, sql, "update batch_image_jobs")
}
