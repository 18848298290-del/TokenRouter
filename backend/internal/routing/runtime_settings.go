package routing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// RuntimeSettingsStore reads routing settings by key.
type RuntimeSettingsStore interface {
	GetValue(context.Context, string) (string, error)
}
type RuntimeSettings struct{ settingRepo RuntimeSettingsStore }

// NewRuntimeSettings stores the reader; settings are loaded when queried.
func NewRuntimeSettings(repo RuntimeSettingsStore) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo}
}

// IsModelFallbackEnabled returns true only for the stored value "true"; read errors disable fallback.
func (s *RuntimeSettings) IsModelFallbackEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEnableModelFallback)
	if err != nil {
		return false // Default: disabled
	}
	return value == "true"
}

// GetFallbackModel returns a platform default on an empty value or read failure, and empty for an unknown platform.
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
	if err != nil || value == "" {
		return defaultModel
	}
	return value
}
