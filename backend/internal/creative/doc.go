// Package creative provides model discovery, submission, execution, and result delivery for image runs.
//
// Reading entry points:
//   - public.go: lists available models, validates requests, and creates runs.
//   - worker.go: consumes queued runs, calls providers, and advances state.
//   - results.go: saves outputs, settles charges, and handles failed runs.
//
// File groups:
//   - public.go, queries.go, run.go: public operations, queries, and run data types.
//   - model_*.go, operation_protocol.go: model settings, group policy, and operation protocols.
//   - executor.go, execution_*.go: execution preparation, output normalization, and retry classification.
//   - worker.go, runtime.go, queue.go: queue consumption and worker lifecycle.
//   - billing.go, results.go, result_delivery.go: funding, settlement, and temporary output delivery.
//   - runtime_settings.go, settings_participant.go, admin_settings_read.go: configuration parsing and validation.
package creative
