package moderation

import (
	"strconv"
	"strings"
)

// Combined administrator settings display risk control and Cyber session
// blocking from the same supplied map. Switch parsing uses exact stored
// strings, while an absent, malformed, or nonpositive session lifetime displays
// one hour.

// AdminReadSettings holds the moderation switches and session lifetime
// displayed by combined administrator settings.
type AdminReadSettings struct {
	CyberSessionBlockEnabled    bool // session blocking display switch
	CyberSessionBlockTTLSeconds int  // seconds
	RiskControlEnabled          bool // risk center display switch
}

// ReadAdminSettings interprets a supplied settings snapshot for administrator
// display. Both switches require the exact string "true"; the TTL accepts
// trimmed positive decimal seconds and otherwise displays one hour, including
// when the key is absent.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.RiskControlEnabled = settings[SettingKeyRiskControlEnabled] == "true"
	result.CyberSessionBlockEnabled = settings[SettingKeyCyberSessionBlockEnabled] == "true"
	// Treat an invalid saved lifetime as a display default so combined settings
	// do not expose a zero-second TTL from a failed decimal conversion.
	if seconds, err := strconv.Atoi(strings.TrimSpace(settings[SettingKeyCyberSessionBlockTTLSeconds])); err == nil && seconds > 0 {
		result.CyberSessionBlockTTLSeconds = seconds
	} else {
		result.CyberSessionBlockTTLSeconds = 3600
	}
	return result
}
