package googleforward

// These exports expose private preparation, policy, and response helpers so
// external fixtures can inspect platform behavior through the HTTP adapter.
var (
	ResolveProjectForTest      = resolveAntigravityProjectID
	EnsureSignatureForTest     = ensureGeminiFunctionCallThoughtSignatures
	ConvertClaudeForTest       = convertClaudeMessagesToGeminiGenerateContent
	GeminiResponseForTest      = (*Gemini).geminiResponseAdapter
	AntigravityResponseForTest = (*Antigravity).antigravityResponseAdapter
	ErrorBodyLimitForTest      = (*Antigravity).upstreamErrorBodyReadLimit
	GeminiFailoverForTest      = (*Gemini).shouldFailoverGeminiUpstreamError
	GeminiPolicyForTest        = (*Gemini).applyGeminiUpstreamErrorPolicy
	AntigravityBodyForTest     = (*Antigravity).buildAntigravityCompatGeminiBody
)

// AttemptForTest exposes request-local attempt observations to external
// forwarding test fixtures.
type AttemptForTest = attempt

// ObserveImagesForTest delegates one payload image observation to the attempt
// counter.
func (a *attempt) ObserveImagesForTest(body []byte) { a.observeImages(body) }

// ImageCountForTest exposes observation and model-name selection for external
// image-count assertions.
func (a *attempt) ImageCountForTest(original, mapped string) int {
	return a.imageCount(original, mapped)
}
