package moderation

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// settings_participant.go prepares moderation fields for combined administrator
// updates while retaining which fields the request supplied. Decoding the input
// map into AdminSettings gives omitted switches the same false value as an
// explicit disabling request. The participant callback keeps the input map to
// distinguish those cases after conversion.
//
// PrepareAdminSettings always produces both boolean keys and includes the
// session lifetime only when positive. The callback then removes writes whose
// fields were absent from the input map, so omission retains a stored switch
// while supplied false disables it. A nonpositive lifetime produces no write
// even when supplied. JSON conversion errors stop preparation before any values
// reach the settings coordinator for persistence.

// Cyber session-block keys store enablement and decimal lifetime seconds for the
// combined administrator update participant and the cached gateway settings reader.
const (
	SettingKeyCyberSessionBlockEnabled    = "cyber_session_block_enabled"
	SettingKeyCyberSessionBlockTTLSeconds = "cyber_session_block_ttl_seconds"
)

// AdminSettings carries the moderation fields accepted by the combined
// administrator settings participant.
type AdminSettings struct {
	// risk center enablement switch
	RiskControlEnabled bool `json:"risk_control_enabled"`
	// Cyber session blocking switch
	CyberSessionBlockEnabled bool `json:"cyber_session_block_enabled"`
	// CyberSessionBlockTTLSeconds is written by PrepareAdminSettings only when
	// positive. Omitted or nonpositive values leave the stored TTL key unchanged
	// through this participant.
	// seconds
	CyberSessionBlockTTLSeconds int `json:"cyber_session_block_ttl_seconds"`
}

// PrepareAdminSettings encodes both switches as boolean strings and a positive
// TTL as decimal seconds. It returns the supplied value unchanged, omits a
// nonpositive TTL key, and performs no storage write or validation failure.
func PrepareAdminSettings(value AdminSettings) (AdminSettings, map[string]string, error) {
	values := map[string]string{}
	values[SettingKeyRiskControlEnabled] = strconv.FormatBool(value.RiskControlEnabled)
	values[SettingKeyCyberSessionBlockEnabled] = strconv.FormatBool(value.CyberSessionBlockEnabled)
	if value.CyberSessionBlockTTLSeconds > 0 {
		values[SettingKeyCyberSessionBlockTTLSeconds] = strconv.Itoa(value.CyberSessionBlockTTLSeconds)
	}

	return value, values, nil
}

// SettingsParticipant registers moderation fields and their storage keys for
// combined administrator updates. Its callback converts supplied JSON fields to
// AdminSettings, then removes generated keys whose corresponding field was
// absent so partial updates retain other settings.
//
// A present false switch survives that presence check and is stored as "false".
// A nonpositive TTL contributes no write even when present. JSON conversion
// errors stop preparation before the coordinator persists the returned values.
func SettingsParticipant() settings.Participant {
	fields := []string{"risk_control_enabled", "cyber_session_block_enabled", "cyber_session_block_ttl_seconds"}
	keys := []string{SettingKeyRiskControlEnabled, SettingKeyCyberSessionBlockEnabled, SettingKeyCyberSessionBlockTTLSeconds}
	return settings.Participant{Module: "moderation", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
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
		// Keep presence separate from decoded zero values: an omitted switch retains
		// its stored value, while a supplied false switch contributes a disabling write.
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}
