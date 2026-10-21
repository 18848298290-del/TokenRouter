// Package bridge adapts conversation formats whose tool calls, reasoning,
// usage, and completion markers occupy different request and event shapes.
// Conversions retain the relationships their target format can represent while
// applying caller-selected sampling, thinking, tool, and schema options.
//
// Request adapters prepare one attempt's payload, and stream adapters retain
// per-conversion state as text and tool arguments arrive separately. Callers
// supply timing or identity sources where generated metadata is needed and own
// transport, authentication, delivery, and retry decisions. Raw JSON helpers
// and typed reserialization have different preservation and validity limits;
// declaration comments identify those limits before the gateway chooses a path.
//
// Reading entry points:
//   - anthropic_to_responses.go converts Messages requests to Responses requests.
//   - chatcompletions_responses_bridge.go converts Chat/Responses messages and tool calls.
//   - gemini_native.go handles Gemini content, tool names, and usage.
//
// File groups:
//   - anthropic_to_responses*.go and responses_to_anthropic*.go handle requests, responses, and stream state.
//   - chatcompletions*.go and responses_to_chatcompletions.go handle Chat requests, responses, and streams.
//   - gemini*.go handle Gemini options, content, and stream events.
//   - responses_client_tools.go, responses_namespace.go, responses_tool_identity.go, and responses_tool_adaptation.go adapt client tools and namespaces.
//   - responses_tool_history.go assigns replay names when historical tool declarations are absent.
//   - responses_input_namespace.go, responses_tool_search_discoveries.go, and client_tools_json.go parse tool input and discoveries.
//   - compat*.go handle continuation, replay, and output observation.
//   - response_format.go and response_forward_wire.go handle response formats and terminal stream output.
//   - runtime.go, request_options.go, and wire_types.go define injected dependencies, conversion options, and protocol types.
package bridge
