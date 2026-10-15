package routing

import (
	settingvalues "github.com/TokenFlux/TokenRouter/internal/settings"
)

// admin_settings_read.go decodes stored routing settings for administrative
// responses.
// Administrative settings responses parse the same routing keys written by the
// settings participant. Missing model strings use platform defaults, the
// fallback flag requires the literal true value, and observation windows are
// normalized before display.

// AdminReadSettings contains resolved model fallback and marketplace
// observation values.
type AdminReadSettings struct {
	EnableModelFallback                  bool   // resolved model fallback flag
	FallbackModelAnthropic               string // resolved Anthropic fallback model
	FallbackModelAntigravity             string // resolved Antigravity fallback model
	FallbackModelGemini                  string // resolved Gemini fallback model
	FallbackModelOpenAI                  string // resolved OpenAI fallback model
	MarketplaceAvailabilityBucketMinutes int    // availability bucket width in minutes
	MarketplaceAvailabilityWindowDays    int    // availability window in days
}

// ReadAdminSettings builds administrative routing values from the stored string
// map. Missing or empty model values use platform defaults; the enable flag
// requires the literal true string, and invalid window values follow
// marketplace normalization. Saved model strings retain their whitespace.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	result.MarketplaceAvailabilityWindowDays, result.MarketplaceAvailabilityBucketMinutes = ParseMarketplaceAvailabilityWindowSettings(settings)
	result.EnableModelFallback = settings[SettingKeyEnableModelFallback] == "true"
	result.FallbackModelAnthropic = settingvalues.StringOrDefault(settings, SettingKeyFallbackModelAnthropic, "claude-3-5-sonnet-20241022")
	result.FallbackModelOpenAI = settingvalues.StringOrDefault(settings, SettingKeyFallbackModelOpenAI, "gpt-4o")
	result.FallbackModelGemini = settingvalues.StringOrDefault(settings, SettingKeyFallbackModelGemini, "gemini-2.5-pro")
	result.FallbackModelAntigravity = settingvalues.StringOrDefault(settings, SettingKeyFallbackModelAntigravity, "gemini-2.5-pro")
	return result
}
