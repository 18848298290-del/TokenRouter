package scheduler

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// SettingsParticipant validates supplied scheduler fields against defaults and prepares setting writes.
func SettingsParticipant(defaults AdminDefaults) settings.Participant {
	keys := []string{SettingKeyAdvancedSchedulerEWMAErrorRateAlpha, SettingKeyAdvancedSchedulerEWMATTFTAlpha, SettingKeyAdvancedSchedulerLBTopK, SettingKeyAdvancedSchedulerStickyEscapeEnabled, SettingKeyAdvancedSchedulerStickyEscapeErrorRate, SettingKeyAdvancedSchedulerStickyEscapeTTFTMs, SettingKeyAdvancedSchedulerStickyWeightedEnabled, SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled, SettingKeyAdvancedSchedulerWeightErrorRate, SettingKeyAdvancedSchedulerWeightLoad, SettingKeyAdvancedSchedulerWeightPreviousResponse, SettingKeyAdvancedSchedulerWeightPriority, SettingKeyAdvancedSchedulerWeightQueue, SettingKeyAdvancedSchedulerWeightQuotaHeadroom, SettingKeyAdvancedSchedulerWeightReset, SettingKeyAdvancedSchedulerWeightSessionSticky, SettingKeyAdvancedSchedulerWeightTTFT}
	return settings.Participant{Module: "scheduler", Fields: keys, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
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
		_, value.AdvancedSchedulerStickyEscapeEnabledSet = input[SettingKeyAdvancedSchedulerStickyEscapeEnabled]
		values, err := PrepareAdminSettings(&value, defaults)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
