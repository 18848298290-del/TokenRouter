package ws

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// SettingsParticipant registers the settings field and validation of the merged effective parameters.
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
