// Package httpapi connects Responses WebSocket requests to session execution, frame conversion, and upstream transports.
//
// Start with:
//   - responses_ws.go: Responses WebSocket HTTP entry handling.
//   - entry_bindings.go: authorization, model selection, and session resources.
//   - openai_ws_forwarder_ingress.go: client connections and per-turn forwarding.
//
// File groups:
//   - openai_ws_executor.go and openai_ws_forwarder.go: executor construction and turn parameters.
//   - openai_ws_ingress_adapter.go and openai_ws_ingress_execution_adapter.go: ingress and upstream adapters.
//   - openai_ws_forwarder_payload.go and openai_ws_request_adapter.go: request headers and payloads.
//   - openai_ws_http_bridge.go: WebSocket turns executed over HTTP and returned as events.
//   - openai_ws_v2_passthrough_adapter.go and openai_ws_passthrough_execution_adapter.go: passthrough adapters.
//   - openai_ws_stream_adapter.go and openai_ws_relay_adapter.go: stream events and relay results.
//   - openai_ws_forwarder_support.go and openai_ws_session_preemption.go: failure attribution and session preemption.
package httpapi
