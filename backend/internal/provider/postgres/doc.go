// Package postgres stores provider configuration, credentials, health state,
// group links, and usage observations in PostgreSQL.
//
// Reading entry points:
//   - provider_store.go binds Ent, SQL, conversion, event, and clock dependencies.
//   - provider_mutations.go coordinates locked configuration merges and
//     transaction-owned events.
//   - provider_queries.go defines administrator filters and gateway scheduling reads.
//
// File groups:
//   - provider_data.go and provider_membership.go create and hydrate records
//     and maintain links.
//   - configuration_write.go, provider_mutations.go, and provider_patches.go
//     merge declared configuration and JSONB patches.
//   - credential_refresh.go, grok_credentials.go, and refresh_*.go compare
//     refresh identities before writing credentials or health.
//   - provider_states.go, managed_recovery.go, and usage_*.go maintain activity,
//     windows, and conditional recovery.
//   - ollama_usage.go and cn_monitor_decision.go persist platform observations
//     and monitor decisions.
//   - events.go and ops_projection.go connect storage changes to scheduling
//     and Ops reads.
//   - group_links.go and proxy_changes.go participate in routing and egress
//     caller transactions.
//   - scheduled_plans.go stores recurring test plans and retained results.
//   - balance_alert.go stores wallet reminder claims and their latest outcomes.
package postgres
