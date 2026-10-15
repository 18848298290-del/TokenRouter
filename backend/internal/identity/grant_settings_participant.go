package identity

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// grant_settings_participant.go converts grant settings to JSON fields and
// prepares submitted grant updates.
// Provider grant settings cross a JSON-fields boundary before the settings
// workflow prepares flat persistence values. Numeric-looking balance text
// must remain a JSON string at that boundary so the merged administrator view
// can retain the stored representation.
//
// Partial input is parsed through the complete provider model for validation,
// which creates defaulted values for missing neighbors. Preparation then
// removes all unsubmitted keys from the encoded result. That final presence
// check prevents those defaults from becoming unrelated writes; empty
// recognized input yields no prepared grant update, while persistence remains
// with the settings coordinator.

// AuthSourceParticipantFields encodes a complete grant view as JSON-string
// fields for settings merging. Nil input returns an allocated empty map;
// numeric and boolean persistence text remains string-valued during JSON
// transport rather than changing representation.
func AuthSourceParticipantFields(value *AuthSourceDefaultSettings) settings.Fields {
	result := settings.Fields{}
	if value == nil {
		return result
	}
	for key, encoded := range encodeAuthSourceSettings(value) {
		// Preserve flat text as JSON strings during settings merge. These encoded
		// values are built-in strings, so unsupported-type or cyclic-value errors cannot
		// arise from the ignored Marshal error here.
		raw, _ := json.Marshal(encoded)
		result[key] = raw
	}
	return result
}

// prepareAuthSourceFields normalizes recognized submitted grant keys for
// persistence. JSON strings are decoded to their stored text; other JSON
// forms retain their raw text for provider parsing. Empty recognized input
// returns nil without validation.
//
// Preparation validates the full parsed provider view, then removes every
// unsubmitted key from its encoded result. This preserves omission during a
// partial settings update even though provider parsing supplies missing
// defaults. Unknown input keys are ignored, and the caller performs the
// eventual write after this function succeeds.
func (g *GrantSettings) prepareAuthSourceFields(ctx context.Context, input settings.Fields) (map[string]string, error) {
	values := map[string]string{}
	for _, key := range AuthSourceSettingKeys() {
		raw, ok := input[key]
		if !ok {
			continue
		}
		var stored string
		// Non-string input can be legacy numeric or boolean JSON, so retain its raw
		// text for the same flat-value parser rather than rejecting that
		// representation.
		if err := json.Unmarshal(raw, &stored); err != nil {
			stored = string(raw)
		}
		values[key] = stored
	}
	if len(values) == 0 {
		return nil, nil
	}
	prepared, err := g.PrepareAuthSourceDefaults(ctx, parseAuthSourceSettings(values))
	if err != nil {
		return nil, err
	}
	// Parsing supplied keys also creates defaults for omitted neighbors; remove
	// those neighbors before persistence so a partial update cannot submit them
	// accidentally.
	for key := range prepared {
		if _, present := values[key]; !present {
			delete(prepared, key)
		}
	}
	return prepared, nil
}
