package search

import (
	"encoding/json"
)

// AdminReadSettings reports whether configured search emulation is enabled.
type AdminReadSettings struct{ WebSearchEmulationEnabled bool }

// ReadAdminSettings reports enabled search emulation when valid settings contain at least one provider.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	if raw := settings[SettingKeyWebSearchEmulationConfig]; raw != "" {
		var wsCfg WebSearchEmulationConfig
		if err := json.Unmarshal([]byte(raw), &wsCfg); err == nil {
			result.WebSearchEmulationEnabled = wsCfg.Enabled && len(wsCfg.Providers) > 0
		}
	}
	return result
}
