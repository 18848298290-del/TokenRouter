package moderation

import (
	"strconv"
	"strings"
)

// AdminReadSettings contains risk-control and Cyber session-block settings.
type AdminReadSettings struct {
	CyberSessionBlockEnabled    bool
	CyberSessionBlockTTLSeconds int
	RiskControlEnabled          bool
}

// ReadAdminSettings parses risk-control settings, defaulting the session-block TTL to one hour.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.RiskControlEnabled = settings[SettingKeyRiskControlEnabled] == "true"
	result.CyberSessionBlockEnabled = settings[SettingKeyCyberSessionBlockEnabled] == "true"
	if seconds, err := strconv.Atoi(strings.TrimSpace(settings[SettingKeyCyberSessionBlockTTLSeconds])); err == nil && seconds > 0 {
		result.CyberSessionBlockTTLSeconds = seconds
	} else {
		result.CyberSessionBlockTTLSeconds = 3600
	}
	return result
}
