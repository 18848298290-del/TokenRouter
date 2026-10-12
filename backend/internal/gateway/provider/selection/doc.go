// Package selection chooses gateway providers by group, model, and runtime state.
//
// Start with:
//   - runtime.go: platform selector construction and shared runtime parameters.
//   - generic.go: candidate filtering, session binding, and load-aware selection.
//   - compatible_picker.go: advanced selection for OpenAI-compatible requests.
//
// File groups:
//   - compatible*.go: compatible candidate policy and scheduler adapters.
//   - generic*.go, gemini*.go: platform selection flows and scheduler dependencies.
//   - contracts.go, native_dependencies.go, runtime*.go: dependencies, provider conversion, and runtime state.
//   - authorized_group.go, candidate_policy.go, parameters.go: groups, eligibility, and scheduler parameters.
//   - sticky.go, openai_ws_forwarder_support.go, long_session_model.go: session binding and long-lived connection models.
//   - diagnostics.go, diagnostic_ports.go, probes.go: scoring diagnostics and availability probes.
//   - free_quota.go, grok*.go, proxy_policy.go, gateway_forward.go: quota, cooldowns, proxies, and model restrictions.
package selection
