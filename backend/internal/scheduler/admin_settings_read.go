package scheduler

import (
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// AdminReadSettings contains stored overrides and effective values for the admin page.
type AdminReadSettings struct {
	AdvancedSchedulerEWMAErrorRateAlpha              string
	AdvancedSchedulerEWMATTFTAlpha                   string
	AdvancedSchedulerEffectiveEWMAErrorRateAlpha     string
	AdvancedSchedulerEffectiveEWMATTFTAlpha          string
	AdvancedSchedulerEffectiveLBTopK                 string
	AdvancedSchedulerEffectiveStickyEscapeEnabled    bool
	AdvancedSchedulerEffectiveStickyEscapeErrorRate  string
	AdvancedSchedulerEffectiveStickyEscapeTTFTMs     string
	AdvancedSchedulerEffectiveWeightErrorRate        string
	AdvancedSchedulerEffectiveWeightLoad             string
	AdvancedSchedulerEffectiveWeightPreviousResponse string
	AdvancedSchedulerEffectiveWeightPriority         string
	AdvancedSchedulerEffectiveWeightQueue            string
	AdvancedSchedulerEffectiveWeightQuotaHeadroom    string
	AdvancedSchedulerEffectiveWeightReset            string
	AdvancedSchedulerEffectiveWeightSessionSticky    string
	AdvancedSchedulerEffectiveWeightTTFT             string
	AdvancedSchedulerLBTopK                          string
	AdvancedSchedulerStickyEscapeEnabled             bool
	AdvancedSchedulerStickyEscapeEnabledSet          bool
	AdvancedSchedulerStickyEscapeErrorRate           string
	AdvancedSchedulerStickyEscapeTTFTMs              string
	AdvancedSchedulerStickyWeightedEnabled           bool
	AdvancedSchedulerSubscriptionPriorityEnabled     bool
	AdvancedSchedulerWeightErrorRate                 string
	AdvancedSchedulerWeightLoad                      string
	AdvancedSchedulerWeightPreviousResponse          string
	AdvancedSchedulerWeightPriority                  string
	AdvancedSchedulerWeightQueue                     string
	AdvancedSchedulerWeightQuotaHeadroom             string
	AdvancedSchedulerWeightReset                     string
	AdvancedSchedulerWeightSessionSticky             string
	AdvancedSchedulerWeightTTFT                      string
}

// RuntimeSettingsFromAdmin parses overrides, records presence, and uses defaults for missing fields.
func RuntimeSettingsFromAdmin(value AdminSettings, defaults AdminDefaults) policy.RuntimeSettings {
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

// ReadAdminSettings parses admin values and normalizes feedback settings.
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

func formatAdminFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
