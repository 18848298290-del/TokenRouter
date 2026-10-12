package team

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// SettingKeyTeamEnabled stores the team feature switch.
const (
	SettingKeyTeamEnabled = "team_enabled"
)

// AdminSettings contains the team feature switch.
type AdminSettings struct {
	TeamEnabled bool `json:"team_enabled"`
}

// PrepareAdminSettings encodes the team feature switch for settings storage.
func PrepareAdminSettings(value AdminSettings) (AdminSettings, map[string]string, error) {
	values := map[string]string{}
	values[SettingKeyTeamEnabled] = strconv.FormatBool(value.TeamEnabled)

	return value, values, nil
}

// SettingsParticipant registers the team setting and prepares fields supplied by the request.
func SettingsParticipant() settings.Participant {
	fields := []string{"team_enabled"}
	keys := []string{SettingKeyTeamEnabled}
	return settings.Participant{Module: "team", Fields: fields, Keys: keys, Prepare: func(_ context.Context, input settings.Fields, _ map[string]string) (settings.PreparedChange, error) {
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
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}

// PublicSettings combines the stored and process team switches and returns the self-service switch.
func PublicSettings(values map[string]string, enabled, selfService bool) (bool, bool) {
	return values[SettingKeyTeamEnabled] != "false" && enabled, selfService
}
