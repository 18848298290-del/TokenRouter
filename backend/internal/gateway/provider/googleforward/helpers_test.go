package googleforward

// These exports expose package internals to external tests.
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

// AttemptForTest exposes attempt state for reset and observation assertions.
type AttemptForTest = attempt

func (a *attempt) ObserveImagesForTest(body []byte) { a.observeImages(body) }

func (a *attempt) ImageCountForTest(original, mapped string) int {
	return a.imageCount(original, mapped)
}
