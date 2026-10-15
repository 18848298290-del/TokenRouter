// Package anthropic prepares and executes Anthropic requests, including OAuth
// identity, protocol recovery, cache usage, and quota observations. Request
// preparation selects authentication-specific Headers and betas before
// sanitizing dependent fields, while response handling reports usage and
// failures to the gateway and provider callers.
//
// Start with execute.go for one provider attempt and request.go for Messages
// construction; stream.go owns streaming response and usage handling.
//
// File groups:
//   - count_tokens_request.go, request_passthrough.go, and exchange_passthrough.go
//     prepare counting and passthrough exchanges alongside the Messages path.
//   - beta.go, claude_oauth_body.go, message_cache.go, and tool_rewrite.go prepare
//     beta-dependent payloads, system instructions, cache markers, and tool names.
//   - billing_header.go, headers.go, cli_version.go, and metadata_userid.go maintain
//     request identity and its wire representations.
//   - oauth_client.go and usage_client.go perform authorization and usage calls;
//     request_fingerprint.go maintains identity stored through package rediscache.
//   - session_digest.go, dateline.go, signature_errors.go, and thinking_budget.go
//     interpret conversation history, date text, and recoverable request errors.
package anthropic
