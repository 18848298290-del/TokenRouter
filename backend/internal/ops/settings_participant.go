package ops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// A combined settings update may supply one Ops field while leaving the other
// monitoring preferences absent. The JSON decoder represents absent switches as
// false, so preparation tracks supplied field names and removes entries created
// solely by zero-value decoding. Positive interval values are included; zero
// and negative interval values produce no stored replacement.
//
// Quota thresholds share the advanced-settings key with retention and display
// preferences. Preparation merges the supplied quota structure into the current
// advanced-settings object before normalization and encoding. The settings
// coordinator receives a prepared change and performs the writes after module
// preparation has completed, while decoding failures stop this participant
// before it supplies replacement values.

// SettingsParticipant registers the Ops fields and prepares their stored
// replacements for a combined settings update. An empty field map produces no
// changes. Scalar switches are persisted only when supplied, and nonpositive
// metrics intervals leave the stored interval untouched.
//
// Quota updates replace the nested quota object within the current
// advanced-settings JSON and normalize the resulting configuration. An exactly
// encoded null quota value is skipped. Preparation reports decoding errors and
// leaves persistence to the settings coordinator, so callers can validate all
// participating modules before applying their collected changes.
func SettingsParticipant() settings.Participant {
	keys := []string{"ops_monitoring_enabled", "ops_realtime_monitoring_enabled", "ops_metrics_interval_seconds", "ops_advanced_settings"}
	fields := []string{"ops_monitoring_enabled", "ops_realtime_monitoring_enabled", "ops_metrics_interval_seconds", "openai_provider_quota_auto_pause"}
	return settings.Participant{Module: "ops", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, current map[string]string) (settings.PreparedChange, error) {
		if len(input) == 0 {
			return settings.PreparedChange{}, nil
		}
		raw, err := json.Marshal(input)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		var value CompositeMonitoringSettings
		if err = json.Unmarshal(raw, &value); err != nil {
			return settings.PreparedChange{}, err
		}
		values := PrepareMonitoringSettings(value)
		// Decoding omitted booleans produces false values. Remove their prepared
		// entries so a partial request does not disable switches that it never
		// supplied.
		for key := range values {
			if _, ok := input[key]; !ok {
				delete(values, key)
			}
		}
		if encoded, ok := input["openai_provider_quota_auto_pause"]; ok && string(encoded) != "null" {
			var quota OpsOpenAIProviderQuotaAutoPauseSettings
			if err = json.Unmarshal(encoded, &quota); err != nil {
				return settings.PreparedChange{}, err
			}
			merged, err := MergeQuotaAutoPauseSettings(current["ops_advanced_settings"], quota)
			if err != nil {
				return settings.PreparedChange{}, err
			}
			encoded, err := json.Marshal(merged)
			if err != nil {
				return settings.PreparedChange{}, fmt.Errorf("marshal ops advanced settings: %w", err)
			}
			values["ops_advanced_settings"] = string(encoded)
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
