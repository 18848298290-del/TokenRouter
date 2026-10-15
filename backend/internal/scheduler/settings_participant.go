package scheduler

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// settings_participant.go prepares partial scheduler settings updates while
// retaining which fields the administrator supplied. Preparation converts the
// request's JSON field map into AdminSettings, validates that typed value
// against process defaults, and returns persisted strings for the supplied
// keys. Omitted fields acquire Go zero values during decoding, so the typed
// object alone cannot distinguish omission from an intentional false value.
//
// The participant records escape-switch presence before preparing the complete
// settings map, allowing a supplied false switch to be encoded as false. It
// then removes every prepared key absent from the request. That final filter
// prevents normalization defaults and omitted zero values from overwriting
// settings outside the administrator's partial update. Validation errors return
// before any prepared change reaches the settings owner.

// SettingsParticipant returns the scheduler's field list and pure update
// preparation callback. Empty input prepares no changes; the callback
// validates a typed scheduler object, records escape-switch presence, and
// returns writes for supplied keys only.
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
		// Typed decoding fills omitted fields with zero values. Remove their prepared
		// writes so a partial update cannot overwrite settings the administrator did not submit.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
