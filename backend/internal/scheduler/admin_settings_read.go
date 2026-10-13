package scheduler

import (
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Administrative reads expose stored override text alongside values derived
// from process defaults. Feedback and escape fields are parsed when valid,
// while the effective Top-K and weight display fields are initialized from
// process defaults. The stored Top-K and weight strings remain separate for
// the management layer to interpret when presenting overrides.
//
// Presence matters independently of a boolean or numeric value.
// ReadAdminSettings marks the escape switch present after successful parsing,
// and RuntimeSettingsFromAdmin carries typed input into the runtime structure.
// That conversion marks numeric presence from nonempty text even when a parser
// returns its fallback, so callers validate administrative input before
// treating the presence flags as evidence of a valid override.

// AdminReadSettings contains stored scheduler text and administrative display
// values. The effective feedback and escape fields incorporate valid stored
// overrides, while effective Top-K and weight fields start from validated
// process defaults.
type AdminReadSettings struct {
	AdvancedSchedulerEWMAErrorRateAlpha             string // stored error blending text
	AdvancedSchedulerEWMATTFTAlpha                  string // stored TTFT blending text
	AdvancedSchedulerEffectiveEWMAErrorRateAlpha    string // resolved error blending text
	AdvancedSchedulerEffectiveEWMATTFTAlpha         string // resolved TTFT blending text
	AdvancedSchedulerEffectiveLBTopK                string // process sample size text
	AdvancedSchedulerEffectiveStickyEscapeEnabled   bool   // resolved escape switch value
	AdvancedSchedulerEffectiveStickyEscapeErrorRate string // resolved escape error fraction
	AdvancedSchedulerEffectiveStickyEscapeTTFTMs    string // resolved escape TTFT milliseconds
	AdvancedSchedulerEffectiveWeightErrorRate       string // process error health weight text
	AdvancedSchedulerEffectiveWeightLoad            string // process provider load weight text
	// process previous response affinity weight text
	AdvancedSchedulerEffectiveWeightPreviousResponse string
	AdvancedSchedulerEffectiveWeightPriority         string // process provider priority weight text
	AdvancedSchedulerEffectiveWeightQueue            string // process waiting count weight text
	AdvancedSchedulerEffectiveWeightQuotaHeadroom    string // process quota headroom weight text
	AdvancedSchedulerEffectiveWeightReset            string // process window reset weight text
	AdvancedSchedulerEffectiveWeightSessionSticky    string // process session affinity weight text
	AdvancedSchedulerEffectiveWeightTTFT             string // process observed TTFT weight text
	AdvancedSchedulerLBTopK                          string // stored sample size text
	AdvancedSchedulerStickyEscapeEnabled             bool   // parsed escape switch value
	// AdvancedSchedulerStickyEscapeEnabledSet is true after stored switch text
	// parses successfully. Missing or invalid text leaves the flag false and
	// keeps the process switch value.
	AdvancedSchedulerStickyEscapeEnabledSet      bool   // valid escape switch presence
	AdvancedSchedulerStickyEscapeErrorRate       string // stored escape error text
	AdvancedSchedulerStickyEscapeTTFTMs          string // stored escape TTFT text
	AdvancedSchedulerStickyWeightedEnabled       bool   // stored weighted affinity switch
	AdvancedSchedulerSubscriptionPriorityEnabled bool   // stored subscription preference switch
	AdvancedSchedulerWeightErrorRate             string // stored error health weight text
	AdvancedSchedulerWeightLoad                  string // stored provider load weight text
	// stored previous response affinity weight text
	AdvancedSchedulerWeightPreviousResponse string
	AdvancedSchedulerWeightPriority         string // stored provider priority weight text
	AdvancedSchedulerWeightQueue            string // stored waiting count weight text
	AdvancedSchedulerWeightQuotaHeadroom    string // stored quota headroom weight text
	AdvancedSchedulerWeightReset            string // stored window reset weight text
	AdvancedSchedulerWeightSessionSticky    string // stored session affinity weight text
	AdvancedSchedulerWeightTTFT             string // stored observed TTFT weight text
}

// RuntimeSettingsFromAdmin converts typed administrative values into runtime
// overrides using the supplied process defaults. Valid numeric text replaces
// its fallback, while invalid or empty text yields the parser's fallback
// value. Callers validate the input before publishing it as runtime settings.
//
// Numeric presence flags follow trimmed text presence, not the parser's
// success flag. This preserves the administrative distinction between supplied
// values and inherited fields, but an unvalidated nonempty value can therefore
// carry a presence flag alongside a fallback value. Weight parsing separately
// skips invalid entries, and the escape switch preserves its explicit
// input-presence flag.
func RuntimeSettingsFromAdmin(value AdminSettings, defaults AdminDefaults) policy.RuntimeSettings {
	// Parsing selects process values for malformed or absent numeric input. Presence
	// is recorded separately below so administrative omission remains distinguishable from supplied text.
	errorAlpha, _ := ParseAdvancedSchedulerAlphaOverride(value.AdvancedSchedulerEWMAErrorRateAlpha, defaults.Process.EwmaErrorRateAlpha)
	ttftAlpha, _ := ParseAdvancedSchedulerAlphaOverride(value.AdvancedSchedulerEWMATTFTAlpha, defaults.Process.EwmaTTFTAlpha)
	stickyTTFT, _ := ParseAdvancedSchedulerPositiveFloatOverride(value.AdvancedSchedulerStickyEscapeTTFTMs, defaults.Process.StickyEscape.TtftMs)
	stickyRate, _ := ParseAdvancedSchedulerRateOverride(value.AdvancedSchedulerStickyEscapeErrorRate, defaults.Process.StickyEscape.ErrorRate)
	return policy.RuntimeSettings{
		StickyWeightedEnabled: value.AdvancedSchedulerStickyWeightedEnabled, SubscriptionPriorityEnabled: value.AdvancedSchedulerSubscriptionPriorityEnabled, LbTopKOverride: ParsePositiveIntOverride(value.AdvancedSchedulerLBTopK),
		EwmaErrorRateAlpha: errorAlpha, EwmaErrorRateAlphaSet: strings.TrimSpace(value.AdvancedSchedulerEWMAErrorRateAlpha) != "", EwmaTTFTAlpha: ttftAlpha, EwmaTTFTAlphaSet: strings.TrimSpace(value.AdvancedSchedulerEWMATTFTAlpha) != "",
		StickyEscapeEnabled: value.AdvancedSchedulerStickyEscapeEnabled, StickyEscapeEnabledSet: value.AdvancedSchedulerStickyEscapeEnabledSet, StickyEscapeTTFTMs: stickyTTFT, StickyEscapeTTFTMsSet: strings.TrimSpace(value.AdvancedSchedulerStickyEscapeTTFTMs) != "", StickyEscapeErrorRate: stickyRate, StickyEscapeErrorRateSet: strings.TrimSpace(value.AdvancedSchedulerStickyEscapeErrorRate) != "", StickyEscape: policy.StickyEscapeConfig{Enabled: value.AdvancedSchedulerStickyEscapeEnabled, TtftMs: stickyTTFT, ErrorRate: stickyRate},
		WeightOverrides: ParseAdvancedSchedulerWeightOverrides(map[string]string{
			SettingKeyAdvancedSchedulerWeightErrorRate:        value.AdvancedSchedulerWeightErrorRate,
			SettingKeyAdvancedSchedulerWeightLoad:             value.AdvancedSchedulerWeightLoad,
			SettingKeyAdvancedSchedulerWeightPreviousResponse: value.AdvancedSchedulerWeightPreviousResponse,
			SettingKeyAdvancedSchedulerWeightPriority:         value.AdvancedSchedulerWeightPriority,
			SettingKeyAdvancedSchedulerWeightQueue:            value.AdvancedSchedulerWeightQueue,
			SettingKeyAdvancedSchedulerWeightQuotaHeadroom:    value.AdvancedSchedulerWeightQuotaHeadroom,
			SettingKeyAdvancedSchedulerWeightReset:            value.AdvancedSchedulerWeightReset,
			SettingKeyAdvancedSchedulerWeightSessionSticky:    value.AdvancedSchedulerWeightSessionSticky,
			SettingKeyAdvancedSchedulerWeightTTFT:             value.AdvancedSchedulerWeightTTFT,
		}),
	}
}

// ReadAdminSettings prepares stored overrides and process-derived display
// values for administrative reads. It trims numeric strings, accepts a
// parseable escape switch, and substitutes valid feedback and threshold
// overrides in their effective display fields. The ordinary affinity and
// subscription switches require the stored string to equal true exactly.
//
// Effective Top-K and weight fields are initialized from the process defaults;
// their stored override strings remain available separately. This distinction
// lets the management consumer retain editable inheritance text while reading
// default display values. Invalid feedback or escape text stays visible as
// stored text but leaves the applicable process-derived effective value in
// place.
func ReadAdminSettings(settings map[string]string, defaults AdminDefaults) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.AdvancedSchedulerStickyWeightedEnabled = settings[SettingKeyAdvancedSchedulerStickyWeightedEnabled] == "true"
	result.AdvancedSchedulerSubscriptionPriorityEnabled = settings[SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled] == "true"
	result.AdvancedSchedulerEWMAErrorRateAlpha = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerEWMAErrorRateAlpha])
	result.AdvancedSchedulerEWMATTFTAlpha = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerEWMATTFTAlpha])
	result.AdvancedSchedulerStickyEscapeTTFTMs = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerStickyEscapeTTFTMs])
	result.AdvancedSchedulerStickyEscapeErrorRate = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerStickyEscapeErrorRate])
	processSchedulerDefaults := defaults.Process
	result.AdvancedSchedulerStickyEscapeEnabled = processSchedulerDefaults.StickyEscape.Enabled
	if raw := strings.TrimSpace(settings[SettingKeyAdvancedSchedulerStickyEscapeEnabled]); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			result.AdvancedSchedulerStickyEscapeEnabled = parsed
			result.AdvancedSchedulerStickyEscapeEnabledSet = true
		}
	}
	result.AdvancedSchedulerEffectiveEWMAErrorRateAlpha = formatAdminFloat(NormalizeFeedbackConfig(FeedbackConfig{
		ErrorRateAlpha: processSchedulerDefaults.EwmaErrorRateAlpha,
		TtftAlpha:      processSchedulerDefaults.EwmaTTFTAlpha,
	}).ErrorRateAlpha)
	result.AdvancedSchedulerEffectiveEWMATTFTAlpha = formatAdminFloat(NormalizeFeedbackConfig(FeedbackConfig{
		ErrorRateAlpha: processSchedulerDefaults.EwmaErrorRateAlpha,
		TtftAlpha:      processSchedulerDefaults.EwmaTTFTAlpha,
	}).TtftAlpha)
	result.AdvancedSchedulerEffectiveStickyEscapeEnabled = processSchedulerDefaults.StickyEscape.Enabled
	result.AdvancedSchedulerEffectiveStickyEscapeTTFTMs = formatAdminFloat(processSchedulerDefaults.StickyEscape.TtftMs)
	result.AdvancedSchedulerEffectiveStickyEscapeErrorRate = formatAdminFloat(processSchedulerDefaults.StickyEscape.ErrorRate)
	// Update effective numeric display values after successful parsing. Invalid stored
	// text remains editable in the raw fields while the process-derived display stays available.
	if parsed, ok := ParseAdvancedSchedulerAlphaOverride(result.AdvancedSchedulerEWMAErrorRateAlpha, processSchedulerDefaults.EwmaErrorRateAlpha); ok {
		result.AdvancedSchedulerEffectiveEWMAErrorRateAlpha = formatAdminFloat(parsed)
	}
	if parsed, ok := ParseAdvancedSchedulerAlphaOverride(result.AdvancedSchedulerEWMATTFTAlpha, processSchedulerDefaults.EwmaTTFTAlpha); ok {
		result.AdvancedSchedulerEffectiveEWMATTFTAlpha = formatAdminFloat(parsed)
	}
	if parsed, ok := ParseAdvancedSchedulerPositiveFloatOverride(result.AdvancedSchedulerStickyEscapeTTFTMs, processSchedulerDefaults.StickyEscape.TtftMs); ok {
		result.AdvancedSchedulerEffectiveStickyEscapeTTFTMs = formatAdminFloat(parsed)
	}
	if parsed, ok := ParseAdvancedSchedulerRateOverride(result.AdvancedSchedulerStickyEscapeErrorRate, processSchedulerDefaults.StickyEscapeErrorRate); ok {
		result.AdvancedSchedulerEffectiveStickyEscapeErrorRate = formatAdminFloat(parsed)
	}
	if raw := strings.TrimSpace(settings[SettingKeyAdvancedSchedulerStickyEscapeEnabled]); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			result.AdvancedSchedulerEffectiveStickyEscapeEnabled = parsed
		}
	}
	result.AdvancedSchedulerLBTopK = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerLBTopK])
	result.AdvancedSchedulerWeightPriority = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightPriority])
	result.AdvancedSchedulerWeightLoad = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightLoad])
	result.AdvancedSchedulerWeightQueue = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightQueue])
	result.AdvancedSchedulerWeightErrorRate = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightErrorRate])
	result.AdvancedSchedulerWeightTTFT = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightTTFT])
	result.AdvancedSchedulerWeightReset = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightReset])
	result.AdvancedSchedulerWeightQuotaHeadroom = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightQuotaHeadroom])
	result.AdvancedSchedulerWeightPreviousResponse = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightPreviousResponse])
	result.AdvancedSchedulerWeightSessionSticky = strings.TrimSpace(settings[SettingKeyAdvancedSchedulerWeightSessionSticky])
	result.AdvancedSchedulerEffectiveLBTopK = EffectiveAdminTopK(defaults)
	effectiveWeights := EffectiveAdminWeights(defaults)
	result.AdvancedSchedulerEffectiveWeightPriority = formatAdminFloat(effectiveWeights.Priority)
	result.AdvancedSchedulerEffectiveWeightLoad = formatAdminFloat(effectiveWeights.Load)
	result.AdvancedSchedulerEffectiveWeightQueue = formatAdminFloat(effectiveWeights.Queue)
	result.AdvancedSchedulerEffectiveWeightErrorRate = formatAdminFloat(effectiveWeights.ErrorRate)
	result.AdvancedSchedulerEffectiveWeightTTFT = formatAdminFloat(effectiveWeights.TTFT)
	result.AdvancedSchedulerEffectiveWeightReset = formatAdminFloat(effectiveWeights.Reset)
	result.AdvancedSchedulerEffectiveWeightQuotaHeadroom = formatAdminFloat(effectiveWeights.QuotaHeadroom)
	result.AdvancedSchedulerEffectiveWeightPreviousResponse = formatAdminFloat(effectiveWeights.PreviousResponse)
	result.AdvancedSchedulerEffectiveWeightSessionSticky = formatAdminFloat(effectiveWeights.SessionSticky)

	return result
}

// formatAdminFloat formats administrative floats in decimal notation without
// unnecessary trailing digits.
func formatAdminFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
