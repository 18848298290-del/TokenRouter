// Package batchimage manages submission, polling, settlement, and cleanup of batch image jobs.
//
// Reading entry points:
//   - public.go: validates requests, selects providers, and reserves funds.
//   - worker.go: consumes queued jobs, maintains locks, and schedules retries.
//   - pipeline.go: connects provider processing with terminal settlement.
//
// File groups:
//   - job.go, contracts.go: job states, items, repository interfaces, and request/response types.
//   - public.go, public_values.go: public operations and response conversion.
//   - provider_values.go, registry.go, value_helpers.go: provider registration and job field conversion.
//   - worker.go, queue.go, runtime.go: queue consumption and background loop lifecycle.
//   - processor.go, pipeline.go: upstream polling, result parsing, and settlement handoff.
//   - billing.go, pricing.go, settlement.go, recovery.go: funding and stalled-job recovery.
//   - download.go, cleanup.go: image and ZIP downloads and expired input/output cleanup.
package batchimage
