package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTeamMigrationContainsIsolationAndAttributionConstraints inspects the initial
// team schema SQL. It requires active-membership index expressions, attribution
// foreign-key names, and both soft-delete and hard-delete owner-protection
// trigger names in the embedded migration.
func TestTeamMigrationContainsIsolationAndAttributionConstraints(t *testing.T) {
	content, err := FS.ReadFile("221_add_teams.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Active membership and owner lookup use partial index declarations. Require
	// their names and predicates to remain in the migration's initial team schema.
	require.Contains(t, sql, "team_memberships_active_user_uq")
	require.Contains(t, sql, "on team_memberships (user_id) where left_at is null")
	require.Contains(t, sql, "team_memberships_active_owner_uq")
	require.Contains(t, sql, "where left_at is null and role = 'owner'")

	// Team requests need stored payer and tenant identities across keys, usage,
	// and batch jobs. Require the named foreign-key declarations for that attribution.
	require.Contains(t, sql, "api_keys_team_id_fkey")
	require.Contains(t, sql, "usage_logs_billing_user_id_fkey")
	require.Contains(t, sql, "usage_logs_team_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_billing_user_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_team_id_fkey")

	// Active owners need protection during both physical and retained deletion.
	// Require the named function and the two user-deletion triggers in this SQL.
	require.Contains(t, sql, "prevent_active_team_owner_deletion")
	require.Contains(t, sql, "users_prevent_active_team_owner_soft_delete")
	require.Contains(t, sql, "users_prevent_active_team_owner_hard_delete")
}

// TestTeamDefaultMemberLimitsMigration inspects team default quota additions.
// It requires the daily, weekly, and monthly limit names and their shared
// database constraint name, preserving those declarations in the embedded SQL.
func TestTeamDefaultMemberLimitsMigration(t *testing.T) {
	content, err := FS.ReadFile("222_add_team_default_member_limits.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// New members copy three period defaults from the team. Require all three
	// column names and their shared constraint name in the default-limit migration.
	require.Contains(t, sql, "default_daily_limit_usd")
	require.Contains(t, sql, "default_weekly_limit_usd")
	require.Contains(t, sql, "default_monthly_limit_usd")
	require.Contains(t, sql, "teams_default_member_limits_check")
}

// TestTeamLifecycleAndAllowanceMigration reads the lifecycle-hardening migration
// SQL. It requires owner-disablement and allowance fields, rejects the separate
// reservation-table name, and checks for owner-protection and membership/key
// update fragments during user removal.
func TestTeamLifecycleAndAllowanceMigration(t *testing.T) {
	content, err := FS.ReadFile("223_harden_team_lifecycle_and_allowance.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Owner status and reserved batch allowance are stored on existing records.
	// Require their field names and reject the alternative reservation-table name.
	require.Contains(t, sql, "team_owner_disabled")
	require.Contains(t, sql, "allowance_reserved")
	require.NotContains(t, sql, "batch_image_billing_reservations")

	// User-removal handling touches the owner check, memberships, and team keys.
	// Require those names and update fragments in the same embedded migration.
	require.Contains(t, sql, "team_owner_transfer_required")
	require.Contains(t, sql, "update team_memberships")
	require.Contains(t, sql, "update api_keys")
}

// TestContentModerationTeamAttributionMigration inspects moderation attribution
// schema and backfill SQL. It requires two payer/team field pairs, four
// ON DELETE SET NULL actions, query-index names, and backfill fragments while rejecting
// reassignment of the historical enforcement user.
func TestContentModerationTeamAttributionMigration(t *testing.T) {
	content, err := FS.ReadFile("225_content_moderation_team_attribution.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Moderation logs and Cyber warnings each gain payer and team identities.
	// Count both declarations to protect coverage of the two stored record types.
	require.Equal(t, 2, strings.Count(sql, "add column if not exists billing_user_id bigint"))
	require.Equal(t, 2, strings.Count(sql, "add column if not exists team_id bigint"))

	// Referenced payers or teams can be removed while audit records remain.
	// Require four foreign-key names and four ON DELETE SET NULL action fragments.
	require.Contains(t, sql, "content_moderation_logs_billing_user_id_fkey")
	require.Contains(t, sql, "content_moderation_logs_team_id_fkey")
	require.Contains(t, sql, "content_moderation_cyber_warnings_billing_user_id_fkey")
	require.Contains(t, sql, "content_moderation_cyber_warnings_team_id_fkey")
	require.Equal(t, 4, strings.Count(sql, "on delete set null"))

	// Payer and team audit listings need timestamp indexes for each record type.
	// Require all four named indexes so neither source is omitted from the migration.
	require.Contains(t, sql, "idx_content_moderation_logs_billing_user_created_at")
	require.Contains(t, sql, "idx_content_moderation_logs_team_created_at")
	require.Contains(t, sql, "idx_content_moderation_cyber_warnings_billing_user_created_at")
	require.Contains(t, sql, "idx_content_moderation_cyber_warnings_team_created_at")

	// Log and Cyber-warning backfills use different attribution sources. Require
	// user/key fragments for logs and usage-record fragments for Cyber warnings,
	// while rejecting direct warning-payer replacement with the enforcement user.
	require.Contains(t, sql, "set billing_user_id = user_id")
	require.Contains(t, sql, "set team_id = keys.team_id")
	require.Contains(t, sql, "from usage_logs as usage")
	require.Contains(t, sql, "set billing_user_id = usage.billing_user_id")
	require.Contains(t, sql, "keys.team_id is null")
	require.NotContains(t, sql, "update content_moderation_cyber_warnings\nset billing_user_id = user_id")

	// The enforcement user is separate from payer and team attribution. Reject
	// user_id assignment text that would merge those identities during this backfill.
	require.NotContains(t, sql, "set user_id =")
}
