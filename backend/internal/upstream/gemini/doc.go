// Package gemini exchanges Gemini generation, image, and batch payloads for
// native and compatible client protocols. Provider callers supply credential
// snapshots, endpoint policy, and request transport; executors own final body
// closing and return independent usage, semantic-output, model, and timing
// observations.
//
// Reading entry points:
//   - execute.go: Executes the selected protocol and collects output observations.
//   - request.go: Builds request plans, authentication, and upstream payloads.
//   - response.go: Parses Gemini responses and writes output events.
//
// File groups:
//   - exchange*.go: Streaming and buffered Messages, Gemini, and OpenAI exchanges.
//   - response*.go: Response reads, protocol output, and completion state.
//   - request.go, url.go, model_get.go: Request construction, model URLs, and model queries.
//   - images.go, image_observation.go: Image generation, decoding, and output counts.
//   - batch*.go: Batch API calls and JSONL encoding.
//   - quota_observation.go, retry_rules.go: Quota signals and retry conditions.
package gemini
