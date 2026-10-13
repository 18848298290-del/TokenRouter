// Package googleforward executes selected-provider Gemini and Antigravity attempts.
// Each request retains its own tool and image observations while injected provider
// services supply credentials, transport, and health feedback for platform retry
// and recovery. HTTP presentation remains synchronous with the calling request.
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
