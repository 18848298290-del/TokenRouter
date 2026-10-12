package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompositeAPIKeyMigrationConstraints(t *testing.T) {
	content, err := FS.ReadFile("226_add_composite_api_keys.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// A false default keeps existing keys in ordinary mode without a backfill.
	require.Contains(t, sql, "is_composite boolean not null default false")

	// Cascading deletes prevent orphaned authentication mappings when a key or group is removed.
	require.Contains(t, sql, "api_key_id bigint not null references api_keys(id) on delete cascade")
	require.Contains(t, sql, "group_id bigint not null references groups(id) on delete cascade")

	// Unique group and normalized-prefix indexes reject conflicting concurrent writes for a key.
	require.Contains(t, sql, "on api_key_composite_groups(api_key_id, group_id)")
	require.Contains(t, sql, "on api_key_composite_groups(api_key_id, normalized_prefix)")
	require.Contains(t, sql, "on api_key_composite_groups(group_id)")

	// Composite keys cannot also use the ordinary single-group binding.
	require.Contains(t, sql, "check (not is_composite or group_id is null)")

	// Persist the effective group and client model so queries, logs, and idempotency checks can recover
	// composite identity.
	require.Contains(t, sql, "add column if not exists group_id bigint")
	require.Contains(t, sql, "add column if not exists requested_model varchar(512)")
	require.Contains(t, sql, "alter column requested_model set default ''")
	require.Contains(t, sql, "alter column requested_model set not null")
	require.Contains(t, sql, "batch_image_jobs_group_id_fkey")
	require.Contains(t, sql, "foreign key (group_id) references groups(id) on delete set null")
}
