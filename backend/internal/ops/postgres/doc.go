// Package postgres stores operational observations and answers monitoring
// queries through the application SQL pool. Dashboard overviews can combine
// covered hourly buckets with raw request fragments, while the other query
// adapters read original rows; cleanup and alert storage use their own
// statement and transaction rules.
//
// Start reading at:
//   - ops_repo.go: Store construction and error and system log queries.
//   - ops_repo_dashboard.go: dashboard queries and aggregate merging.
//   - cleanup.go: time-based batch cleanup.
//
// File groups:
//   - ops_repo*.go: Ops queries, alert rules, events, and silences.
//   - collector_queries.go, ops_sla_sql.go: metric sampling and SLA filters.
//   - ops_ingress_reject_repo.go: batch writes and queries for ingress rejection counts.
//   - cleanup.go, historical_ingress_cleanup.go: expired logs and historical ingress rejection cleanup.
//   - advisory.go: PostgreSQL advisory locks for maintenance tasks.
package postgres
