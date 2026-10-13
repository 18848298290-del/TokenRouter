package creative

// Administrator reads interpret the feature flag independently from model
// admission. Missing worker counts use the pool default, while missing or
// malformed model settings yield an empty allowlist that closes the creative
// directory.

// AdminReadSettings contains the creative feature switch, model settings, and
// worker count.
type AdminReadSettings struct {
	CreativeEnabled       bool                   // feature switch read result
	CreativeModelSettings []CreativeModelSetting // normalized model allowlist entries
	CreativeWorkerCount   int                    // configured task worker count
}

// ReadAdminSettings parses creative values for the administration response.
// Only the exact string false disables the feature; model parsing fails closed
// and invalid worker counts use DefaultCreativeWorkerCount.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.CreativeModelSettings = ParseCreativeModelSettings(settings[SettingKeyCreativeModelSettings])
	result.CreativeWorkerCount = ParseCreativeWorkerCount(settings[SettingKeyCreativeWorkerCount])
	result.CreativeEnabled = settings[SettingKeyCreativeEnabled] != "false"

	return result
}
