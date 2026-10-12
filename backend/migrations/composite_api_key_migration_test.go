package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCompositeAPIKeyMigrationConstraints inspects composite-key relationship and
// job snapshot SQL. It requires ordinary-mode defaults, cascading relationship
// references, pair indexes, the mutually exclusive group check, and historical
// job-group references using ON DELETE SET NULL with a retained requested model.
func TestCompositeAPIKeyMigrationConstraints(t *testing.T) {
	content, err := FS.ReadFile("226_add_composite_api_keys.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Existing keys retain ordinary mode when the schema gains this flag; the
	// migration must supply the false default used by that compatibility path.
	require.Contains(t, sql, "is_composite boolean not null default false")

	// A composite membership belongs to both its key and group. Require cascading
	// references so physically removing either parent also removes the relationship.
	require.Contains(t, sql, "api_key_id bigint not null references api_keys(id) on delete cascade")
	require.Contains(t, sql, "group_id bigint not null references groups(id) on delete cascade")

	// A key resolves each group and normalized prefix through a distinct mapping.
	// Require both pair indexes and the group lookup index in the embedded SQL.
	require.Contains(t, sql, "on api_key_composite_groups(api_key_id, group_id)")
	require.Contains(t, sql, "on api_key_composite_groups(api_key_id, normalized_prefix)")
	require.Contains(t, sql, "on api_key_composite_groups(group_id)")

	// The two binding modes share the key row. Require the check that leaves
	// group_id unset when is_composite selects relationship-based group bindings.
	require.Contains(t, sql, "check (not is_composite or group_id is null)")

	// Job snapshots retain the effective group and requested model for subsequent
	// queries. Require the requested-model default and the historical group reference's
	// ON DELETE SET NULL action in the embedded migration.
	require.Contains(t, sql, "add column if not exists group_id bigint")
	require.Contains(t, sql, "add column if not exists requested_model varchar(512)")
	require.Contains(t, sql, "alter column requested_model set default ''")
	require.Contains(t, sql, "alter column requested_model set not null")
	require.Contains(t, sql, "batch_image_jobs_group_id_fkey")
	require.Contains(t, sql, "foreign key (group_id) references groups(id) on delete set null")
}
