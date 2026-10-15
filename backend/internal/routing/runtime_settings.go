package routing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// runtime_settings.go reads runtime model-fallback settings and supplies
// platform defaults when needed.
// Model fallback reads the stored enable flag and platform-specific model when
// a request needs them. Read failures disable the flag or select the built-in
// platform model, keeping unavailable settings from producing an empty fallback
// for a recognized platform.

// RuntimeSettingsStore reads persisted routing values for the requested setting
// key.
type RuntimeSettingsStore interface {
	// GetValue returns the persisted string associated with the setting key.
	GetValue(context.Context, string) (string, error)
}

// RuntimeSettings loads model fallback policy through its configured settings
// reader.
type RuntimeSettings struct {
	settingRepo RuntimeSettingsStore // persisted routing settings reader
}

// NewRuntimeSettings retains the settings reader for subsequent runtime
// queries. It performs no read during construction; callers provide a non-nil
// reader before invoking methods that access stored routing settings.
func NewRuntimeSettings(repo RuntimeSettingsStore) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo}
}

// IsModelFallbackEnabled reports whether the saved flag equals the literal
// string true. A settings read error disables fallback, and other spellings or
// surrounding whitespace are treated as an unenabled value.
func (s *RuntimeSettings) IsModelFallbackEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEnableModelFallback)
	// A failed flag read leaves fallback disabled because enabling model
	// substitution requires an affirmative stored policy value for the request.
	if err != nil {
		return false
	}
	return value == "true"
}

// GetFallbackModel reads the saved fallback model for a recognized provider
// platform. Empty values and read failures select that platform's built-in
// model; an unsupported platform returns an empty string without reading
// settings.
func (s *RuntimeSettings) GetFallbackModel(ctx context.Context, platform string) string {
	var key string
	var defaultModel string

	switch platform {
	case capability.PlatformAnthropic:
		key = SettingKeyFallbackModelAnthropic
		defaultModel = "claude-3-5-sonnet-20241022"
	case capability.PlatformOpenAI:
		key = SettingKeyFallbackModelOpenAI
		defaultModel = "gpt-4o"
	case capability.PlatformGemini:
		key = SettingKeyFallbackModelGemini
		defaultModel = "gemini-2.5-pro"
	case capability.PlatformAntigravity:
		key = SettingKeyFallbackModelAntigravity
		defaultModel = "gemini-2.5-pro"
	default:
		return ""
	}

	value, err := s.settingRepo.GetValue(ctx, key)
	// A recognized platform retains a nonempty built-in model when its stored
	// choice is missing or the settings reader cannot complete the lookup.
	if err != nil || value == "" {
		return defaultModel
	}
	return value
}
