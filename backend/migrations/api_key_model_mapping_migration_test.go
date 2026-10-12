package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAPIKeyModelMappingMigration inspects the embedded model-mapping migration.
// It requires an empty JSONB default, cleanup of NULL mappings, and the
// not-null constraint needed for existing and newly created key records.
func TestAPIKeyModelMappingMigration(t *testing.T) {
	content, err := FS.ReadFile("231_add_api_key_model_mapping.sql")
	require.NoError(t, err)

	sql := strings.ToLower(strings.Join(strings.Fields(string(content)), " "))
	require.Contains(t, sql, "add column if not exists model_mapping jsonb not null default '{}'::jsonb")
	require.Contains(t, sql, "where model_mapping is null")
	require.Contains(t, sql, "alter column model_mapping set not null")
}
