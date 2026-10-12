package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeRunsAllowanceReservedMigration checks that creative runs record allowance reservations.
// The flag represents the same reservation state as batch_image_jobs.allowance_reserved.
func TestCreativeRunsAllowanceReservedMigration(t *testing.T) {
	content, err := FS.ReadFile("254_creative_runs_allowance_reserved.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Existing runs default to having no recorded reservation.
	require.Contains(t, sql, "alter table creative_runs add column if not exists allowance_reserved boolean not null default false")
}
