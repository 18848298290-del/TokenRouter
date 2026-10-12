// Package bridge converts Anthropic, OpenAI, and Gemini requests, responses, and stream events.
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
//   - responses_client_tools.go, responses_namespace.go, and responses_tool_adaptation.go adapt client tools and namespaces.
//   - responses_input_namespace.go, responses_tool_search_discoveries.go, and client_tools_json.go parse tool input and discoveries.
//   - compat*.go handle continuation, replay, and output observation.
//   - response_format.go and response_forward_wire.go handle response formats and terminal stream output.
//   - runtime.go, request_options.go, and wire_types.go define injected dependencies, conversion options, and protocol types.
package bridge
