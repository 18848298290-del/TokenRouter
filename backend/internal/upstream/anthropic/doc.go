// Package anthropic prepares and executes Anthropic requests, including OAuth
// identity, protocol recovery, cache usage, and quota observations. Request
// preparation selects authentication-specific Headers and betas before
// sanitizing dependent fields, while response handling reports usage and
// failures to the gateway and provider callers.
//
// Start with execute.go for one provider attempt and request.go for Messages
// construction; stream.go owns streaming response and usage handling.
// CountTokens and passthrough request paths sit alongside their exchange
// variants. Beta, OAuth body, message cache, and tool rewrite files prepare
// payloads; billing, Headers, CLI version, and metadata files maintain request
// identity. OAuth and usage clients perform platform HTTP calls, Redis adapters
// store fingerprints, and digest, dateline, signature, and thinking helpers
// interpret request or error data.
package anthropic
