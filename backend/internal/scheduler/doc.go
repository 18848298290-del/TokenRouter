// Package scheduler chooses eligible providers and manages the capacity and
// cache state that selection needs. Gateway adapters supply admitted groups,
// platform checks, and execution-record associations; the scheduler orders
// metadata, owns acquired resources, and returns either a usable provider or a
// waiting plan.
//
// Start with selection.go for acquire, fresh-read, and database-recheck
// ownership, snapshot.go for rebuild and outbox recovery, and concurrency.go
// for request slots and connection leases. Basic and advanced selection share
// eligibility supplied by their adapters, while scoring and feedback use
// credential-free metadata. A returned candidate remains subject to the
// execution caller's completion, cancellation, and retry rules.
//
// File groups:
//   - basic_selection.go, scoring.go, generic_selection*.go,
//     platform_selection*.go, platform_basic_selection.go, and
//     gemini_selection.go order eligible metadata.
//   - selection.go, selection_context.go, lease.go, runtime.go,
//     bucket_lease.go, and live_lease.go own selection inputs, request
//     resources, and worker lifetimes.
//   - snapshot*.go, bucket.go, and outbox.go manage snapshots, bucket identity,
//     event consumption, and rebuilding.
//   - concurrency.go, wait*.go, image_limiter.go, and message_queue*.go manage
//     request admission and waiting.
//   - session*.go and sticky.go track session limits, provider bindings, and
//     escape decisions.
//   - rpm*.go, provider_rpm.go, and user_rpm_cache.go control
//     request-per-minute admission and spacing.
//   - admin_settings*.go, settings*.go, parameters.go, and
//     validation_weights.go read, validate, and merge scheduler policy.
//   - diagnostics.go and diagnostic_service.go observe operations and explain
//     administrative scores.
package scheduler
