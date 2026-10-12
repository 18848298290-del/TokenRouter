package identity

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// AuthSourceParticipantFields encodes merged grant settings using the persistent value format.
func AuthSourceParticipantFields(value *AuthSourceDefaultSettings) settings.Fields {
	result := settings.Fields{}
	if value == nil {
		return result
	}
	for key, encoded := range encodeAuthSourceSettings(value) {
		// Encode stored values as JSON strings so legacy numeric text is not parsed as a JSON number.
		raw, _ := json.Marshal(encoded)
		result[key] = raw
	}
	return result
}

// prepareAuthSourceFields validates and prepares the supplied provider-grant fields for persistence.
func (g *GrantSettings) prepareAuthSourceFields(ctx context.Context, input settings.Fields) (map[string]string, error) {
	values := map[string]string{}
	for _, key := range AuthSourceSettingKeys() {
		raw, ok := input[key]
		if !ok {
			continue
		}
		var stored string
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
	for key := range prepared {
		if _, present := values[key]; !present {
			delete(prepared, key)
		}
	}
	return prepared, nil
}
