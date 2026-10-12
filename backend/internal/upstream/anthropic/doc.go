// Package anthropic builds Anthropic requests and handles OAuth, responses,
// cache usage, and rate-limit observations.
//
// Start with execute.go for execution, request.go for Messages request building,
// and stream.go for streaming responses and usage. Request and exchange files
// include count_tokens and passthrough paths. Beta policy, OAuth body, message
// cache, and tool rewrite files prepare request fields. Billing, headers, CLI
// version, and metadata files maintain the client fingerprint. OAuth and usage
// clients provide platform calls; session_digest.go hashes conversation input.
// Dateline, signature, and thinking helpers normalize or classify protocol data.
package anthropic
