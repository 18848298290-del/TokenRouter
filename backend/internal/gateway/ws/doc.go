// Package ws coordinates Responses WebSocket sessions, per-turn forwarding, and admission.
//
// Start with:
//   - entry.go: admission, provider selection, and failover after upgrade.
//   - ingress.go: turn parsing, connection leases, HTTP bridging, and response recovery.
//   - passthrough.go: bidirectional relay, per-turn usage, and completion callbacks.
//
// File groups:
//   - entry*.go: entry orchestration, dependency interfaces, and retry decisions.
//   - ingress*.go and openai_session_contract.go: ingress session state and turn callbacks.
//   - normalize*.go, service_tier_frame.go, and policy_error.go: request normalization and policy checks.
//   - passthrough*.go, stream*.go, and relay_contract.go: frame relay and single-turn response forwarding.
//   - client*.go and deadline.go: client I/O, frame filters, and timeouts.
//   - turn*.go and usage*.go: turn state, retry errors, and usage snapshots.
//   - preemption.go: session preemption, lease renewal, and lease-loss notification.
//   - parameters.go, runtime.go, and settings.go: parameter validation, refresh, and publication.
package ws
