// Package usage queries usage records and statistics, maintains rollups and cleanup tasks, and controls ranking settings.
//
// Reading entry points:
//   - service.go queries usage by user, API key, provider, and request filters.
//   - dashboard_service.go queries dashboard statistics, trends, and rankings.
//   - dashboard_aggregation_service.go schedules rollups, historical backfill, and deletion repair.
//
// File groups:
//   - log*.go, request_type.go, and views.go define usage records, query results, request types, and display data.
//   - repository.go, query_readers.go, and options.go define persistence, query capabilities, and service configuration.
//   - dashboard*.go and query_cache.go implement dashboard queries, caches, and aggregation jobs.
//   - analytics_aggregation.go and aggregation_state.go define rollup storage, watermarks, and backfill progress updates.
//   - cleanup*.go creates, executes, and cancels batched usage deletion tasks.
//   - provider*.go defines provider statistics and rolling token windows.
//   - ranking.go, runtime_settings.go, settings_participant.go, and admin_settings_read.go control rankings and user error visibility.
package usage
