// Package qoder adapts Qoder authentication, encrypted requests, streaming responses, and conversation state.
//
// Entry points:
//   - execute.go prepares upstream requests and emits the requested client protocol.
//   - client.go sends signed requests, parses SSE, and classifies API errors.
//   - payload.go converts Chat, Messages, and Responses requests into Qoder payloads.
//
// File groups:
//   - auth*.go, oauth.go, credential_builder.go, refresh_exchange.go: PAT, device login, credentials, and refresh.
//   - session.go, signature.go, encoding.go: encrypted sessions, signing, and body encoding.
//   - site.go, models.go: site profiles, model aliases, and capabilities.
//   - client.go, execute.go, output_observation.go: transport, execution, cancellation, and output observation.
//   - payload.go, stream_conversion.go: request conversion and client protocol output.
//   - conversation.go: conversation identity, incremental history, commits, and rollback.
//   - quota_usage.go: quota response fields and numeric parsing.
package qoder
