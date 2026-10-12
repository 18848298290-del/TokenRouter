// Package ws coordinates Responses WebSocket admission, per-turn forwarding,
// and connection recovery. A client connection contains separate logical
// requests, each with captured model and accounting data and its own admission
// lifetime.
//
// Entry retains the Key reservation across a classified provider retry while
// releasing user and provider slots between turns. Sequential ingress owns HTTP
// bridge replay or pooled connection affinity; dedicated passthrough uses a
// terminal-delivery barrier while the two relay directions progress
// independently. Protocol adapters provide parsing, provider rules, and
// transport exchange through the declared interfaces. Per-turn snapshots keep
// completion attached to accepted bytes even when shared session defaults
// change.
//
// Start here:
//   - entry.go: follows upgraded admission, provider replacement, and
//     completion callbacks.
//   - ingress.go: follows sequential turn execution, bridge history, pooled
//     leases, and continuation recovery.
//   - passthrough.go: follows bidirectional filtering, captured usage, and
//     terminal delivery admission.
//
// File groups:
//   - entry*.go: defines entry operations, selected targets, and retry or
//     billing-model rules.
//   - ingress*.go and openai_session_contract.go: defines session state and the
//     HTTP-adapter turn callback data.
//   - normalize*.go, service_tier_frame.go, and policy_error.go: validates
//     request shapes and applies group, model, image, and tier policies.
//   - passthrough*.go, stream*.go, and relay_contract.go: forwards dedicated or
//     pooled response events and retains completion observations.
//   - client*.go and deadline.go: handles client closure, frame filtering, and
//     per-phase upstream timeouts.
//   - turn*.go and usage*.go: orders terminal delivery, captures request
//     metadata, and classifies retry errors.
//   - request_records.go: retains logical request IDs across attempts and
//     finishes incomplete turns on connection exit.
//   - preemption.go: replaces scoped ownership and observes remote-token loss
//     through cache polling with per-check context deadlines.
//   - parameters.go, runtime.go, and settings.go: merges overrides, validates
//     effective parameters, and publishes generation-checked runtime snapshots.
package ws
