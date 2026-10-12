// Package postgres stores usage records, computes reports, and maintains dashboard and analytics rollups.
//
// Reading entry points:
//   - usage_log_repo.go constructs the store and defines query filters and ranking entry points.
//   - usage_log_repo_insert.go writes usage records, batches queued writes, and retries database failures.
//   - dashboard_aggregation_repo.go maintains dashboard rollups and usage partitions.
//
// File groups:
//   - usage_log_repo*.go implements usage writes, reads, statistics, trends, and dashboard queries.
//   - dashboard_aggregation_repo.go and usage_analytics_aggregation_repo.go aggregate, backfill, and clean rollups.
//   - provider_report.go and team_reports.go query provider and team reports.
//   - api_key_dashboard_analytics.go and key_totals.go query API-key dashboard statistics and usage totals.
//   - usage_cleanup_repo.go stores cleanup tasks and deletes usage records.
//   - support.go defines SQL interfaces, entity conversion, and transaction-client selection.
package postgres
