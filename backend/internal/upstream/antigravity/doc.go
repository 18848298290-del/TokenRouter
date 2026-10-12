// Package antigravity forwards Claude and Gemini requests through Antigravity v1internal.
//
// Start with:
//   - execute.go: one upstream exchange and client-protocol output.
//   - request_transformer.go: Claude-to-Gemini conversion and the v1internal envelope.
//   - retry_loop.go: rate limits, capacity exhaustion, and retries.
//
// File groups:
//   - oauth.go, client.go: OAuth configuration, tokens, tiers, and model queries.
//   - request_transformer.go, payload.go, gemini_types.go: request formats, model mapping, and schema cleanup.
//   - response*.go, stream_transformer.go: streaming and non-streaming response conversion.
//   - retry*.go, quota_errors.go, recovery*.go: retries, quota errors, and request recovery.
//   - execute.go, probe.go: execution and provider connection probes.
package antigravity
