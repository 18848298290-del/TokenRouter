// Package googleforward executes Gemini and Antigravity attempts and same-provider recovery.
//
// Start with:
//   - gemini_attempts.go: Gemini Messages and GenerateContent entry points.
//   - antigravity_messages.go: Antigravity Messages conversion and retry.
//
// File groups:
//   - gemini_*.go: Gemini forwarding, OpenAI compatibility, and responses.
//   - antigravity_*.go: Antigravity conversion, signature recovery, and responses.
//   - model_attributes.go: sampling and reasoning parameters from resolved model attributes.
package googleforward
