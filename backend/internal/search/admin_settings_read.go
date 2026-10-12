package search

import (
	"encoding/json"
)

// The combined administrator settings view exposes whether stored search
// emulation is enabled and contains at least one supplier. It derives that flag
// directly from the serialized setting; supplier credentials, quota
// eligibility, and Manager publication are handled by the search runtime.

// AdminReadSettings contains the search-enabled flag used by combined
// administrator settings.
type AdminReadSettings struct {
	// WebSearchEmulationEnabled is true when stored JSON enables search and lists
	// at least one provider; this display check does not validate supplier
	// eligibility.
	WebSearchEmulationEnabled bool
}

// ReadAdminSettings derives the combined settings flag from stored search JSON.
// Missing, empty, or malformed values leave the flag false; decoded
// configuration requires Enabled and a nonempty provider list.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	if raw := settings[SettingKeyWebSearchEmulationConfig]; raw != "" {
		var wsCfg WebSearchEmulationConfig
		// Treat malformed stored JSON as a disabled display flag so combined settings
		// can still render the other independently read settings.
		if err := json.Unmarshal([]byte(raw), &wsCfg); err == nil {
			result.WebSearchEmulationEnabled = wsCfg.Enabled && len(wsCfg.Providers) > 0
		}
	}
	return result
}
