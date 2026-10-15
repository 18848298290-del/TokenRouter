package team

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// settings_participant.go prepares administrator team settings and computes
// public feature visibility.
// The stored team switch controls the public feature view together with
// process enablement. PublicSettings treats only exact false text as disabled
// and returns self-service visibility separately, while administrator
// preparation writes canonical boolean strings.
//
// A participant decodes only supplied request fields into the team settings
// value, then removes keys whose fields were omitted. This keeps an omitted
// switch from becoming a false write through the zero-valued decoded struct.
// The returned metadata slices are also used by its preparation closure, so
// callers keep them stable. TeamService's action gate separately reads its
// retained startup options rather than this public settings map.

// SettingKeyTeamEnabled identifies the persisted public feature switch
// combined with process team enablement for browser-facing settings.
const (
	SettingKeyTeamEnabled = "team_enabled"
)

// AdminSettings represents the submitted team feature switch for
// administrator settings preparation.
type AdminSettings struct {
	// TeamEnabled is encoded as canonical true or false text by
	// PrepareAdminSettings. PublicSettings combines that stored switch with the
	// process-enabled input for advertisement.
	TeamEnabled bool `json:"team_enabled"`
}

// PrepareAdminSettings returns the scalar settings value and a fresh map
// encoding its switch as boolean text. It performs no persistence or
// validation and returns nil error for either boolean value.
func PrepareAdminSettings(value AdminSettings) (AdminSettings, map[string]string, error) {
	values := map[string]string{}
	values[SettingKeyTeamEnabled] = strconv.FormatBool(value.TeamEnabled)

	return value, values, nil
}

// SettingsParticipant declares the team-enabled request field and its
// corresponding persistence key. Empty input produces no prepared change;
// supplied raw fields are marshaled and decoded into AdminSettings, with
// conversion failures returned before preparation.
//
// The decoded boolean becomes canonical saved text, but omission is checked
// against raw field presence before that value is accepted for writing. A
// supplied null boolean decodes to false, while an absent field is removed
// from the output. Fields and Keys share their slices with the preparation
// closure, so callers keep that metadata stable; the settings registry owns
// eventual persistence.
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
		// Remove the key for each omitted raw field after typed decoding,
		// preventing the decoded zero boolean from overwriting a team switch the
		// request did not submit.
		for i, field := range fields {
			if _, ok := input[field]; !ok {
				delete(values, keys[i])
			}
		}
		return settings.PreparedChange{Values: values}, nil
	}}
}

// PublicSettings returns public team visibility and the supplied self-service
// flag. Only exact stored false text disables the saved switch; absence or
// other text still needs process enablement, and self-service is returned
// independently even when team visibility is false.
func PublicSettings(values map[string]string, enabled, selfService bool) (bool, bool) {
	// Interpret exact saved false alongside process enablement for the public
	// view, retaining independent self-service input even when the resulting
	// team feature is hidden.
	return values[SettingKeyTeamEnabled] != "false" && enabled, selfService
}
