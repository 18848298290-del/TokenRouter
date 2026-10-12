// Package scheduler selects providers, maintains scheduling snapshots, and manages request slots and session limits.
//
// Start here:
//   - selection.go acquires slots in candidate order, rechecks providers, and records attempt leases.
//   - snapshot.go reads and rebuilds provider snapshots and consumes scheduling outbox events.
//   - concurrency.go acquires provider and user slots and tracks API key concurrency and wait queues.
//
// File groups:
//   - basic_selection.go, scoring.go, generic_selection*.go, platform_selection*.go, platform_basic_selection.go, and gemini_selection.go filter and rank candidates.
//   - selection.go, selection_context.go, lease.go, runtime.go, bucket_lease.go, and live_lease.go manage selection inputs, request resources, and worker lifecycles.
//   - snapshot*.go, bucket.go, and outbox.go define snapshot access, bucket identities, events, and rebuilds.
//   - concurrency.go, wait*.go, image_limiter.go, and message_queue*.go manage concurrency and queues.
//   - session*.go and sticky.go track sessions, bindings, and sticky escape decisions.
//   - rpm*.go, provider_rpm.go, and user_rpm_cache.go enforce request-per-minute limits.
//   - admin_settings*.go, settings*.go, parameters.go, and validation_weights.go read, validate, and merge settings.
//   - diagnostics.go and diagnostic_service.go emit logs and explain scheduling scores.
package scheduler
