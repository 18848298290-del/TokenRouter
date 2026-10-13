package routing

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Routing settings writes encode model fallback choices alongside bounded
// marketplace observation windows. The settings participant converts supplied
// fields through AdminSettings, applies window normalization, and keeps the
// resulting writes limited to the keys present in the submitted update.
//
// The typed conversion can introduce defaults for omitted values, so those
// generated keys are removed before the participant returns its prepared
// change. This preserves partial-update behavior for unrelated settings while
// still validating the representation of supplied values. Persistence and
// transaction handling belong to the settings service that consumes the
// participant.

// Model fallback keys select whether requests may substitute a model and which
// saved model each recognized provider platform uses.
const (
	SettingKeyEnableModelFallback      = "enable_model_fallback"
	SettingKeyFallbackModelAnthropic   = "fallback_model_anthropic"
	SettingKeyFallbackModelAntigravity = "fallback_model_antigravity"
	SettingKeyFallbackModelGemini      = "fallback_model_gemini"
	SettingKeyFallbackModelOpenAI      = "fallback_model_openai"
)

// AdminSettings contains writable model fallback and marketplace
// observation-window values.
type AdminSettings struct {
	// enable request model substitution
	EnableModelFallback bool `json:"enable_model_fallback"`
	// FallbackModelAnthropic, FallbackModelAntigravity, FallbackModelGemini, and
	// FallbackModelOpenAI save model IDs verbatim. RuntimeSettings and
	// ReadAdminSettings use platform defaults for empty strings; supplied
	// whitespace remains part of the saved value.
	FallbackModelAnthropic string `json:"fallback_model_anthropic"` // Anthropic fallback model ID
	// Antigravity fallback model ID
	FallbackModelAntigravity string `json:"fallback_model_antigravity"`
	FallbackModelGemini      string `json:"fallback_model_gemini"` // Gemini fallback model ID
	FallbackModelOpenAI      string `json:"fallback_model_openai"` // OpenAI fallback model ID
	// MarketplaceAvailabilityBucketMinutes and MarketplaceAvailabilityWindowDays
	// are normalized together so the prepared window meets the marketplace's
	// bounded bucket count, including caller-supplied zero values.
	// availability bucket width in minutes
	MarketplaceAvailabilityBucketMinutes int `json:"marketplace_availability_bucket_minutes"`
	// availability window in days
	MarketplaceAvailabilityWindowDays int `json:"marketplace_availability_window_days"`
}

// PrepareAdminSettings encodes routing settings and normalizes observation
// limits on the supplied value. Pass a non-nil settings pointer; its window and
// bucket fields are updated in place, and the returned map includes every
// routing key.
func PrepareAdminSettings(settings *AdminSettings) map[string]string {
	updates := map[string]string{}
	settings.MarketplaceAvailabilityWindowDays, settings.MarketplaceAvailabilityBucketMinutes = NormalizeMarketplaceAvailabilityWindow(
		settings.MarketplaceAvailabilityWindowDays,
		settings.MarketplaceAvailabilityBucketMinutes,
	)
	updates[SettingKeyMarketplaceAvailabilityWindowDays] = strconv.Itoa(settings.MarketplaceAvailabilityWindowDays)
	updates[SettingKeyMarketplaceAvailabilityBucketMinutes] = strconv.Itoa(settings.MarketplaceAvailabilityBucketMinutes)
	updates[SettingKeyEnableModelFallback] = strconv.FormatBool(settings.EnableModelFallback)
	updates[SettingKeyFallbackModelAnthropic] = settings.FallbackModelAnthropic
	updates[SettingKeyFallbackModelOpenAI] = settings.FallbackModelOpenAI
	updates[SettingKeyFallbackModelGemini] = settings.FallbackModelGemini
	updates[SettingKeyFallbackModelAntigravity] = settings.FallbackModelAntigravity
	return updates
}

// SettingsParticipant registers routing's writable fields with the shared
// settings update service. Its callback accepts partial field maps, converts
// them through AdminSettings, and returns serialization errors before preparing
// stored values.
//
// An empty map produces an empty change. Normalization uses the typed input's
// zero values for omitted fields and does not consult stored settings. For a
// window-only update, the omitted bucket interval therefore uses its default
// while the supplied window is normalized. Generated keys absent from the input
// are then removed, and the settings service persists the supplied keys.
func SettingsParticipant() settings.Participant {
	keys := []string{SettingKeyEnableModelFallback, SettingKeyFallbackModelAnthropic, SettingKeyFallbackModelAntigravity, SettingKeyFallbackModelGemini, SettingKeyFallbackModelOpenAI, SettingKeyMarketplaceAvailabilityBucketMinutes, SettingKeyMarketplaceAvailabilityWindowDays}
	return settings.Participant{Module: "routing", Fields: keys, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value AdminSettings
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		values := PrepareAdminSettings(&value)
		// Typed decoding assigns zero values to omitted fields. Remove their prepared
		// keys so this participant updates exactly the supplied settings fields.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
