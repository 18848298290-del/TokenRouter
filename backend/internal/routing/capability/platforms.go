package capability

// platforms.go defines provider platform identifiers and returns the supported
// platform list.
// Provider platform names select capability tables and constrain
// forced-platform candidate queries. The complete platform list is returned
// with fresh storage so a caller building a cross-platform pool can change its
// slice without altering later queries.

// Platform constants identify provider families used by protocol tables and
// forced-platform filtering across candidate pools.
const (
	PlatformAnthropic   = "anthropic"   // Anthropic provider platform identity
	PlatformOpenAI      = "openai"      // OpenAI provider platform identity
	PlatformGemini      = "gemini"      // Gemini provider platform identity
	PlatformAntigravity = "antigravity" // Antigravity provider platform identity
	PlatformGrok        = "grok"        // Grok provider platform identity
	PlatformQoder       = "qoder"       // Qoder provider platform identity
	PlatformKimi        = "kimi"        // Kimi provider platform identity
	PlatformZhipu       = "zhipu"       // Zhipu provider platform identity
	PlatformDeepseek    = "deepseek"    // DeepSeek provider platform identity
	PlatformJev         = "jev"         // Jev provider platform identity
)

// ProviderPlatforms returns a new slice containing every declared provider
// platform. Callers may change or reorder that slice independently when
// preparing a platform filter for candidate queries.
func ProviderPlatforms() []string {
	return []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformQoder, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformJev}
}
