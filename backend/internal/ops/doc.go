// Package ops collects operational metrics and errors, serves monitoring queries, and runs alerts, reports, and historical cleanup.
//
// Reading entry points:
//   - service.go: error persistence, queries, and runtime settings snapshots.
//   - metrics_collector.go: periodic host, connection pool, and provider concurrency sampling.
//   - dashboard.go: overview queries and health summaries.
//
// File groups:
//   - alert*.go and scheduled_report_service.go: alert rules, threshold evaluation, and email reports.
//   - cleanup*.go and historical_ingress_cleanup.go: cleanup plans, retention, and historical ingress rejection cleanup.
//   - error*.go, ingress_reject.go, and upstream*.go: classification, asynchronous persistence, and ingress rejection counts.
//   - dashboard*.go, health_score.go, histograms.go, trends.go, and token_stats*.go: historical monitoring queries and statistics.
//   - realtime*.go, concurrency.go, and provider_availability.go: live traffic, concurrency, and provider availability.
//   - request_details*.go, models.go, and user_error.go: request details, phase timings, and user error views.
//   - settings*.go, admin_settings_read.go, composite_settings.go, and ignored_status.go: runtime settings and parsing.
//   - system_log*.go and log_runtime.go: system log persistence, queries, and runtime logging configuration.
//   - aggregation_service.go and query_mode.go: metric pre-aggregation and query source selection.
//   - options.go, port.go, collector_ports.go, runtime_ports.go, and auth_health.go: dependencies, construction options, and auth health queries.
//   - release_query.go: release information and rollback candidates.
package ops
