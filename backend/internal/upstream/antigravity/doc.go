// Package antigravity forwards Claude and Gemini generation through
// project-scoped v1internal requests. Forwarding keeps provider retry decisions
// separate from client-protocol conversion, while response observation can retain
// usage from the upstream tail after client output fails.
//
// Reading entry points:
//   - execute.go: Selects one exchange, client output, and attempt observations.
//   - request_transformer.go: Converts Claude input and chooses identity instructions.
//   - retry_loop.go: Selects rate-limit waits, capacity retries, and provider switching.
//
// File groups:
//   - oauth.go and client.go: OAuth credentials, endpoint availability, and account queries.
//   - payload.go and gemini_types.go: Request preparation and shared wire aliases.
//   - response*.go and stream_transformer.go: Native and compatible response adaptation.
//   - retry*.go, quota_errors.go, and recovery*.go: Provider-local recovery decisions.
//   - probe.go: Connection probes using the forwarding retry adapter.
package antigravity
