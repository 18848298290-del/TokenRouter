// Package apikey manages API keys, authenticates callers, and checks request limits.
//
// Reading entry points:
//   - api_key.go defines keys, status, usage windows, and copying.
//   - service.go creates, reads, and updates keys and checks team access and usage limits.
//   - authenticate.go authenticates keys and resolves request identities.
//
// File groups:
//   - auth_cache*.go caches authentication snapshots and refreshes them through invalidation and the outbox.
//   - admin.go, managed_key.go, and credential_rotation.go handle admin operations, managed keys, and rotation.
//   - composite.go, model_mapping.go, and runtime_group.go handle composite groups, model redirects, and request group selection.
//   - access_context.go, request_metadata.go, and reauthenticate.go store request identity and metadata and revalidate access.
//   - request_limits.go, ip.go, and invalid_auth_abuse_limiter.go enforce request concurrency, IP rules, and invalid-auth limits.
//   - options.go, clock.go, and lifecycle.go configure the service and manage time and lifecycle.
//   - snapshot_copy.go and published_invalidation.go copy snapshot data and publish cache invalidation.
package apikey
