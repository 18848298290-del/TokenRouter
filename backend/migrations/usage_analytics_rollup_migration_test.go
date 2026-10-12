package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUsageAnalyticsRollupMigrationAvoidsLargeTableRewrite inspects analytics table
// and setting SQL. It requires rollup/state table declarations and setting
// cleanup, while rejecting usage-log mutation and immediate hourly backfill
// statement patterns.
func TestUsageAnalyticsRollupMigrationAvoidsLargeTableRewrite(t *testing.T) {
	content, err := FS.ReadFile("229_usage_analytics_rollups.sql")
	require.NoError(t, err)

	sql := strings.ToUpper(string(content))
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS USAGE_ANALYTICS_HOURLY")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS USAGE_ANALYTICS_DAILY")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS USAGE_ANALYTICS_AGGREGATION_STATE")
	require.NotContains(t, sql, "ALTER TABLE USAGE_LOGS")
	require.NotContains(t, sql, "UPDATE USAGE_LOGS")
	require.NotContains(t, sql, "INSERT INTO USAGE_ANALYTICS_HOURLY SELECT")
	require.Contains(t, sql, "WHERE KEY = 'OPS_ADVANCED_SETTINGS'")
	require.Contains(t, sql, "DELETE FROM SETTINGS WHERE KEY = 'OPS_QUERY_MODE_DEFAULT'")
}

// TestManualBackfillStateMigrationAvoidsUsageLogs inspects manual backfill cursor
// additions. It requires changes to the aggregation-state table and the start
// and cursor names, while rejecting SQL fragments reading or changing usage logs.
func TestManualBackfillStateMigrationAvoidsUsageLogs(t *testing.T) {
	content, err := FS.ReadFile("230_pre_aggregation_manual_backfill.sql")
	require.NoError(t, err)

	sql := strings.ToUpper(string(content))
	require.Contains(t, sql, "ALTER TABLE USAGE_ANALYTICS_AGGREGATION_STATE")
	require.Contains(t, sql, "MANUAL_BACKFILL_START")
	require.Contains(t, sql, "MANUAL_BACKFILL_CURSOR")
	require.NotContains(t, sql, "ALTER TABLE USAGE_LOGS")
	require.NotContains(t, sql, "UPDATE USAGE_LOGS")
	require.NotContains(t, sql, "FROM USAGE_LOGS")
}

// TestUsageAnalyticsModelDimensionResetInvalidatesOldBuckets inspects aggregate-reset
// SQL. It requires hourly/daily truncation and reset watermark, coverage, and
// cursor fields, while rejecting update or source-query fragments targeting
// the raw usage logs.
func TestUsageAnalyticsModelDimensionResetInvalidatesOldBuckets(t *testing.T) {
	content, err := FS.ReadFile("232_reset_usage_analytics_model_dimension.sql")
	require.NoError(t, err)

	sql := strings.ToUpper(string(content))
	require.Contains(t, sql, "TRUNCATE TABLE USAGE_ANALYTICS_HOURLY, USAGE_ANALYTICS_DAILY")
	require.Contains(t, sql, "UPDATE USAGE_ANALYTICS_AGGREGATION_STATE")
	require.Contains(t, sql, "LIVE_WATERMARK = TIMESTAMPTZ '1970-01-01 00:00:00+00'")
	require.Contains(t, sql, "COVERAGE_START = NULL")
	require.Contains(t, sql, "BACKFILL_CURSOR = NULL")
	require.NotContains(t, sql, "UPDATE USAGE_LOGS")
	require.NotContains(t, sql, "FROM USAGE_LOGS")
}
