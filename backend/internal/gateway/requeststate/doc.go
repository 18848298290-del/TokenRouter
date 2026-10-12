// Package requeststate parses gateway payloads and captures execution evidence
// shared across provider attempts. Request-body views retain raw bytes
// alongside scalar fields, while context snapshots keep entry identity,
// selection hints, and admitted routing decisions available to later execution.
//
// A body rewrite requires refreshed parsed ranges, and group replacement can
// leave a saved plan that attempt resolution rechecks against the current
// group. Tool state separates active output from future WebSocket declarations
// and publishes retained maps with caller immutability requirements. Presence
// flags keep omitted hints and uninitialized tool state distinct from
// explicitly supplied zero or empty values.
//
// Reading entry points:
//   - body.go: request fields, messages, and session identifiers.
//   - execution_hints.go: request-context execution flags.
//   - routing.go: groups, protocols, and model routing plans.
//
// File groups:
//   - body.go, openai_view.go, user_message.go: body views and user-message extraction.
//   - routing.go, attempt_route.go, model_body.go: plans, candidate resolution, and model replacement.
//   - reasoning_effort*.go, thinking.go: effort levels, request policies, and thinking state.
//   - execution_hints.go, health.go, credential_budget.go: hints, health observations, and credential deadlines.
//   - guardian_affinity.go, gemini_signature.go: parent-session affinity and provider-signature cleanup.
//   - codex_bootstrap.go, response_tools.go: Codex bootstrap inputs and tool declarations.
//   - response_failure.go: one-time consumption of provider failure effects.
package requeststate
