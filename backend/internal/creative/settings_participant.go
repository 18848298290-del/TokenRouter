package creative

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Creative administrator requests can update one field while keeping the
// other stored settings. Encoding a partial Fields map into AdminSettings gives
// omitted fields their Go zero values, and preparation expands that typed
// record into a complete map of persisted setting strings. Presence information
// therefore has to survive alongside the typed conversion.
//
// The participant validates and normalizes the typed values, then removes
// persisted keys whose request fields were absent. A supplied false flag,
// defaulted worker count, or empty model allowlist can still be written when
// its field was present. This separates a deliberate administrator update from
// an omitted field before the settings owner persists the batch.

// These persisted keys store the feature switch, worker pool target, and exact
// group/model allowlist prepared by the creative administrator settings participant.
const (
	// runtime feature enable switch.
	SettingKeyCreativeEnabled = "creative_enabled"
	// desired worker pool size.
	SettingKeyCreativeWorkerCount = "creative_worker_count"
	// configured group model allowlist.
	SettingKeyCreativeModelSettings = "creative_model_settings"
)

// AdminSettings contains the enable flag, worker count, and model allowlist.
type AdminSettings struct {
	// requested feature enable switch
	CreativeEnabled bool `json:"creative_enabled"`
	// requested task worker count
	CreativeWorkerCount int `json:"creative_worker_count"`
	// requested model allowlist entries
	CreativeModelSettings []CreativeModelSetting `json:"creative_model_settings"`
}

// PrepareAdminSettings returns normalized creative values and strings ready
// for settings persistence. Nonpositive worker counts use
// DefaultCreativeWorkerCount, while invalid model entries return
// INVALID_CREATIVE_MODEL_SETTINGS before any write occurs.
func PrepareAdminSettings(value AdminSettings) (AdminSettings, map[string]string, error) {
	values := map[string]string{}
	values[SettingKeyCreativeEnabled] = strconv.FormatBool(value.CreativeEnabled)
	raw, normalized, err := MarshalCreativeModelSettings(value.CreativeModelSettings)
	if err != nil {
		return value, nil, apperror.BadRequest("INVALID_CREATIVE_MODEL_SETTINGS", err.Error())
	}
	value.CreativeModelSettings = normalized
	if value.CreativeWorkerCount <= 0 {
		value.CreativeWorkerCount = DefaultCreativeWorkerCount
	}
	values[SettingKeyCreativeModelSettings] = raw
	values[SettingKeyCreativeWorkerCount] = strconv.Itoa(value.CreativeWorkerCount)

	return value, values, nil
}

// SettingsParticipant returns the creative request fields, persisted keys,
// and a preparation callback for partial settings updates. Empty input returns
// an empty change; encoding, decoding, or model validation errors stop
// preparation before the settings owner writes values.
//
// The callback converts supplied fields to AdminSettings, then retains the
// original field map to distinguish omission from typed zero values. After
// normalization it removes persisted keys for absent request fields, so an
// omitted switch or allowlist cannot overwrite a stored value merely because
// its typed representation is false or empty.
func SettingsParticipant() settings.Participant {
	fields := []string{"creative_enabled", "creative_worker_count", "creative_model_settings"}
	keys := []string{SettingKeyCreativeEnabled, SettingKeyCreativeWorkerCount, SettingKeyCreativeModelSettings}
	return settings.Participant{Module: "creative", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
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
		_, values, err := PrepareAdminSettings(value)
		if err != nil {
			return settings.PreparedChange{}, err
		}
		// Preparing a typed record gives omitted fields zero values. Remove their
		// corresponding keys so a partial administrator request keeps
		// stored values
		// owned by fields that were absent from the request.
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
