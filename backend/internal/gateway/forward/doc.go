// Package forward prepares one provider attempt, converts its protocol output,
// and returns usage or failure observations to the surrounding gateway loop.
// The loop owns provider selection and remaining retry budgets; forwarding
// retains logical model input separately from selected upstream names and raw
// response declarations. HTTP, credential, health, and output operations are
// supplied by request adapters so these flows can run with their own captured
// response state.
//
// Response reading can retain usage alongside a failure, and a selected failover
// signal changes whether that partial result is usable. Streaming conversion
// also distinguishes produced terminal state, writer commitment, and byte
// progress. These observations describe an attempt's handling; callers combine
// them with their own completion and recovery rules. Protocol bridge algorithms
// live in protocol/bridge, while this package orders preparation and selects the
// response/output path.
//
// Reading entrypoints:
//   - messages.go orders Messages preparation, HTTP decisions, and stream outcomes.
//   - conversion.go prepares OpenAI requests for Anthropic; conversion_output.go
//     accumulates or streams the converted response.
//   - failure.go defines classifications and retry instructions consumed by gateway/failover.
//
// File groups:
//   - messages*.go, gemini*.go, and count*.go pair execution flows with adapter operations.
//   - anthropic*.go handles API-key passthrough and health-before-display errors.
//   - conversion*.go, output.go, and mimic.go handle request adaptation, SSE framing,
//     downstream writing, and OAuth client preparation.
//   - failure.go, grok_credential_failure.go, invalid_json.go, qoder_error.go, and
//     upstream_warning.go carry classified failures or captured warning evidence.
//   - result.go, openai_result.go, messages_result.go, and response_observer.go keep
//     usage, replay values, and raw model/tier observations for completion.
package forward
