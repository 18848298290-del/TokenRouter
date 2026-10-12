package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTeamMigrationContainsIsolationAndAttributionConstraints(t *testing.T) {
	content, err := FS.ReadFile("221_add_teams.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Partial unique indexes enforce one active team per user and one active owner per team.
	require.Contains(t, sql, "team_memberships_active_user_uq")
	require.Contains(t, sql, "on team_memberships (user_id) where left_at is null")
	require.Contains(t, sql, "team_memberships_active_owner_uq")
	require.Contains(t, sql, "where left_at is null and role = 'owner'")

	// Keys, usage records, and batch image jobs retain team and payer attribution.
	require.Contains(t, sql, "api_keys_team_id_fkey")
	require.Contains(t, sql, "usage_logs_billing_user_id_fkey")
	require.Contains(t, sql, "usage_logs_team_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_billing_user_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_team_id_fkey")

	// Deleting an owner requires ownership transfer or team dissolution first.
	require.Contains(t, sql, "prevent_active_team_owner_deletion")
	require.Contains(t, sql, "users_prevent_active_team_owner_soft_delete")
	require.Contains(t, sql, "users_prevent_active_team_owner_hard_delete")
}

func TestTeamDefaultMemberLimitsMigration(t *testing.T) {
	content, err := FS.ReadFile("222_add_team_default_member_limits.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Daily, weekly, and monthly default limits need database checks rejecting negative values.
	require.Contains(t, sql, "default_daily_limit_usd")
	require.Contains(t, sql, "default_weekly_limit_usd")
	require.Contains(t, sql, "default_monthly_limit_usd")
	require.Contains(t, sql, "teams_default_member_limits_check")
}

func TestTeamLifecycleAndAllowanceMigration(t *testing.T) {
	content, err := FS.ReadFile("223_harden_team_lifecycle_and_allowance.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Fields record owner disablement and reserved batch allowance without a separate reservation table.
	require.Contains(t, sql, "team_owner_disabled")
	require.Contains(t, sql, "allowance_reserved")
	require.NotContains(t, sql, "batch_image_billing_reservations")

	// User deletion handles owner protection, member departure, and team-key disablement together.
	require.Contains(t, sql, "team_owner_transfer_required")
	require.Contains(t, sql, "update team_memberships")
	require.Contains(t, sql, "update api_keys")
}

func TestContentModerationTeamAttributionMigration(t *testing.T) {
	content, err := FS.ReadFile("225_content_moderation_team_attribution.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// Both moderation record types need payer and team fields.
	require.Equal(t, 2, strings.Count(sql, "add column if not exists billing_user_id bigint"))
	require.Equal(t, 2, strings.Count(sql, "add column if not exists team_id bigint"))

	// Deleting a referenced object must retain the audit record.
	require.Contains(t, sql, "content_moderation_logs_billing_user_id_fkey")
	require.Contains(t, sql, "content_moderation_logs_team_id_fkey")
	require.Contains(t, sql, "content_moderation_cyber_warnings_billing_user_id_fkey")
	require.Contains(t, sql, "content_moderation_cyber_warnings_team_id_fkey")
	require.Equal(t, 4, strings.Count(sql, "on delete set null"))

	// Listing queries use time indexes scoped to the payer or team.
	require.Contains(t, sql, "idx_content_moderation_logs_billing_user_created_at")
	require.Contains(t, sql, "idx_content_moderation_logs_team_created_at")
	require.Contains(t, sql, "idx_content_moderation_cyber_warnings_billing_user_created_at")
	require.Contains(t, sql, "idx_content_moderation_cyber_warnings_team_created_at")

	// Local audits can recover the original payer; Cyber warnings use usage records for precise attribution.
	require.Contains(t, sql, "set billing_user_id = user_id")
	require.Contains(t, sql, "set team_id = keys.team_id")
	require.Contains(t, sql, "from usage_logs as usage")
	require.Contains(t, sql, "set billing_user_id = usage.billing_user_id")
	require.Contains(t, sql, "keys.team_id is null")
	require.NotContains(t, sql, "update content_moderation_cyber_warnings\nset billing_user_id = user_id")

	// Attribution backfills must retain the historical user targeted by enforcement.
	require.NotContains(t, sql, "set user_id =")
}
