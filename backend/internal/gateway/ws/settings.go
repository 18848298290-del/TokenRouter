package ws

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Administrative WS updates must validate the combined override object before
// settings storage accepts it. SettingsParticipant claims the responses_ws
// field, merges it with current storage and deployment defaults, and supplies
// prepared values only when that effective configuration passes validation.

// SettingsParticipant registers responses_ws parsing and effective-value
// validation for settings updates. An omitted field produces no prepared
// change; invalid merge or validation returns INVALID_RESPONSES_WS_SETTINGS,
// and valid overrides are returned for the settings transaction to persist.
func SettingsParticipant(defaults Parameters) settings.Participant {
	return settings.Participant{Module: "responses-ws", Fields: []string{SettingKey}, Keys: []string{SettingKey}, Prepare: func(_ context.Context, fields settings.Fields, current map[string]string) (settings.PreparedChange, error) {
		raw, present := fields[SettingKey]
		if !present {
			return settings.PreparedChange{}, nil
		}
		merged, err := PatchParameters(defaults, current[SettingKey], raw)
		if err != nil {
			return settings.PreparedChange{}, apperror.BadRequest("INVALID_RESPONSES_WS_SETTINGS", err.Error())
		}
		return settings.PreparedChange{Values: map[string]string{SettingKey: merged}}, nil
	}}
}
