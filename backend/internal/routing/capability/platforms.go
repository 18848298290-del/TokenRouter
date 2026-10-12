package capability

// PlatformAnthropic and the following constants identify platforms in the capability catalog.
const (
	PlatformAnthropic   = "anthropic"
	PlatformOpenAI      = "openai"
	PlatformGemini      = "gemini"
	PlatformAntigravity = "antigravity"
	PlatformGrok        = "grok"
	PlatformQoder       = "qoder"
	PlatformKimi        = "kimi"
	PlatformZhipu       = "zhipu"
	PlatformDeepseek    = "deepseek"
	PlatformJev         = "jev"
)

// ProviderPlatforms returns an independent platform list for cross-platform candidate pools.
func ProviderPlatforms() []string {
	return []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformQoder, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformJev}
}
