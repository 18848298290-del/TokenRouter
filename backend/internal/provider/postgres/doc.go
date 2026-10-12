// Package postgres stores provider records, credentials, scheduling state, and usage observations in PostgreSQL.
//
// Reading entry points:
//   - provider_store.go constructs the store and supplies configuration and transaction context.
//   - provider_data.go creates and loads providers and converts Ent records.
//   - provider_queries.go implements filtering, pagination, and scheduler candidate queries.
//
// File groups:
//   - provider_*.go, configuration_write.go manage provider records, group links, and configuration writes.
//   - credential_refresh.go, grok_credentials.go, refresh_*.go handle credential refresh, errors, and cooldown cleanup.
//   - usage_*.go, ollama_usage.go, cn_monitor_decision.go, managed_recovery.go handle observations, platform quota, and recovery.
//   - events.go, ops_projection.go handle change events, snapshot propagation, and Ops load reads.
//   - group_links.go, proxy_changes.go update group links and proxies.
//   - scheduled_plans.go stores test schedules and results.
package postgres
