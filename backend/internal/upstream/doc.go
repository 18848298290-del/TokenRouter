// Package upstream defines request execution and output interfaces shared by platform adapters.
//
// Start with:
//   - attempt.go: attempt inputs, results, and the execution interface.
//   - output.go: response headers, bytes, and flushes delivered to the caller's output adapter.
//   - image_request_parse.go: image generation and edit request parsing.
//
// File groups:
//   - attempt.go, output.go, frame.go, responses_observation.go: execution and output observations.
//   - image*.go: image models, request fields, uploads, and response bytes.
//   - error_message.go, model_error.go, stream_error.go: error classification and client messages.
//   - client_tools_stream.go: Responses client-tool stream conversion.
//   - http_profile.go, path_segment.go, session_identity.go, wait.go: transport, paths, sessions, and cancellable waits.
package upstream
