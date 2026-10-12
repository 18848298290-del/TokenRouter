package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunsAllowanceReservedMigration inspects the creative reservation-flag
// migration. It requires a non-null false default so existing creative runs
// begin with no recorded allowance reservation when the column is added.
func TestCreativeRunsAllowanceReservedMigration(t *testing.T) {
	content, err := FS.ReadFile("254_creative_runs_allowance_reserved.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "alter table creative_runs add column if not exists allowance_reserved boolean not null default false")
}
