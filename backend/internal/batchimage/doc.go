// Package batchimage manages asynchronous image jobs from validated submission
// through provider execution, indexed results, funding settlement and resource
// cleanup. PostgreSQL stores lifecycle state and submission snapshots; Redis
// coordinates visits, leases, retries and download permits. Provider resource
// names stay inside persisted records.
//
// Start here:
//   - public.go: validates submission, binds the provider and checks ownership.
//   - worker.go: coordinates queue visits and cancellation through held leases.
//   - processor.go: polls the provider and reconciles results before settlement.
//
// File groups:
//   - job.go and contracts.go: define persisted records and provider/public values.
//   - billing.go, pricing.go and settlement.go: reserve and settle frozen funding.
//   - pipeline.go, recovery.go and runtime.go: resume stages and manage work loops.
//   - download.go and cleanup.go: stream retained results and remove resources.
//   - registry.go and model_attributes.go: select backends and validate model
//     preferences. provider_values.go and public_values.go render persisted
//     values; value_helpers.go supplies nullable-field and model-identity
//     conversions.
package batchimage
