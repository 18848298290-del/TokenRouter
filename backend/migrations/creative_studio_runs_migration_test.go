package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreativeStudioRunsMigration inspects the initial creative-run schema SQL.
// It requires managed-key constraints, run and output metadata, relationship
// declarations, and initial uniqueness indexes, while rejecting six column-text
// patterns for private request or response content.
func TestCreativeStudioRunsMigration(t *testing.T) {
	content, err := FS.ReadFile("253_creative_studio_runs.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// managed_by identifies the application managing an execution key. Require
	// its index and the constraint accepting creative_studio or an unset value.
	require.Contains(t, sql, "alter table api_keys add column if not exists managed_by varchar(32)")
	require.Contains(t, sql, "create index if not exists api_keys_managed_by_idx on api_keys (managed_by)")
	require.Contains(t, sql, "managed_by is null or managed_by = 'creative_studio'")

	// Initial run rows need identity, operation, hashes, funding references, and
	// version data. Require these declarations in the original creative migration.
	require.Contains(t, sql, "create table if not exists creative_runs")
	require.Contains(t, sql, "run_id varchar(64) not null unique")
	require.Contains(t, sql, "operation varchar(16) not null")
	require.Contains(t, sql, "prompt_hash varchar(64) not null")
	require.Contains(t, sql, "request_fingerprint varchar(64) not null unique")
	require.Contains(t, sql, "status varchar(20) not null default 'queued'")
	require.Contains(t, sql, "balance_hold_amount decimal(20,10)")
	require.Contains(t, sql, "subscription_hold_allocations jsonb")
	require.Contains(t, sql, "version bigint not null default 1")
	// The run belongs to its user, execution key, and group. Require cascading
	// user deletion and the execution-key and group references in the SQL.
	require.Contains(t, sql, "user_id bigint not null references users(id) on delete cascade")
	require.Contains(t, sql, "api_key_id bigint not null references api_keys(id)")
	require.Contains(t, sql, "group_id bigint not null references groups(id)")
	// User history and active-run lookup use separate index declarations. Require
	// the descending creation-time index and the queued-or-running predicate text.
	require.Contains(t, sql, "create index if not exists creative_runs_user_created_at_idx on creative_runs (user_id, created_at desc)")
	require.Contains(t, sql, "where status in ('queued', 'running')")
	// The initial schema scopes idempotency keys to a user. Later workspace
	// migrations replace this index; this test protects the original SQL declaration.
	require.Contains(t, sql, "create unique index if not exists creative_runs_idempotency_key_uq on creative_runs (user_id, idempotency_key)")

	// Each output is identified by run and index, with expiry and acknowledgement
	// metadata. Require the pair's uniqueness and cascading run reference together.
	require.Contains(t, sql, "create table if not exists creative_run_outputs")
	require.Contains(t, sql, "references creative_runs(run_id) on delete cascade")
	require.Contains(t, sql, "unique (run_id, output_index)")
	require.Contains(t, sql, "transient_expires_at timestamptz")
	require.Contains(t, sql, "acked_at timestamptz")

	// Image bytes, masks, and plaintext prompts must stay out of the schema.
	for _, forbidden := range []string{"prompt text", "image_bytes", "mask_bytes", "image_data", "source_image", "response_body"} {
		require.NotContains(t, sql, forbidden)
	}
}
