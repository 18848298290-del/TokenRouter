// Package openai executes OpenAI and ChatGPT exchanges after gateway code has
// selected a provider and prepared its policy. HTTP response bodies, first-output
// staging, protocol dispatch, and auxiliary calls retain their own cleanup and
// error rules so callers can distinguish delivered output from served results.
//
// Request builders receive identity, transport, and header callbacks from the
// gateway adapters. Response readers preserve model and tool identities across
// Responses, Chat, and Messages conversions through the shared protocol packages.
// Provider registration and quota state stay with their owning use cases; this
// package supplies the vendor exchanges and the values those callers consume.
//
// Reading entry points:
//   - request_headers.go builds Responses HTTP requests and headers.
//   - response_stream.go reads Responses streams and emits client events.
//   - codex_transform.go normalizes Codex input, tools and instructions.
//
// File groups:
//   - agent_identity*.go, oauth*.go and pat_client.go handle authentication.
//   - codex_identity.go, provider_identity.go, fingerprint.go and allowed_client.go handle client identity and session isolation.
//   - request*.go, responses_lite_tools.go and encrypted_content.go prepare requests and replay data.
//   - response*.go, first_output_stage.go and silent_refusal.go read responses and classify first output.
//   - ws*.go and live*.go handle WebSocket connections, replay and Live authentication.
//   - images*.go, image*.go, embeddings.go and alpha_search*.go handle media and auxiliary requests.
//   - privacy_client.go, quota_client.go and codex_usage_headers.go query account settings and quota.
//   - tool_corrector.go and codex_tool_names.go correct tool names, arguments and responses.
//   - test*.go, count_input_tokens.go and http_exchange.go handle provider probes, token queries and HTTP exchanges.
package openai
