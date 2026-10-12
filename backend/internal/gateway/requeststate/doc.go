// Package requeststate parses gateway requests and holds execution state shared across attempts.
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
