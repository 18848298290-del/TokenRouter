// Package forward prepares single-provider exchanges, converts protocols, and captures failures and usage.
//
// Reading entrypoints:
//   - messages.go prepares Messages exchanges and handles their failures.
//   - conversion.go converts client requests and upstream responses.
//   - failure.go defines failure classification, retry signals, and client errors.
//
// File groups:
//   - messages*.go, gemini*.go, and count*.go implement forwarding and token counting.
//   - anthropic*.go handles passthrough and error display.
//   - conversion*.go, output.go, and mimic.go handle conversion, output, and client mimicry.
//   - failure.go, grok_credential_failure.go, invalid_json.go, qoder_error.go, and upstream_warning.go classify failures.
//   - result.go, openai_result.go, messages_result.go, and response_observer.go hold results and observations.
package forward
