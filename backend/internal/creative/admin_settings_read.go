package creative

// AdminReadSettings contains the creative feature switch, model settings, and worker count.
type AdminReadSettings struct {
	CreativeEnabled       bool
	CreativeModelSettings []CreativeModelSetting
	CreativeWorkerCount   int
}

// ReadAdminSettings parses creative configuration from the supplied settings values.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}
	result.CreativeModelSettings = ParseCreativeModelSettings(settings[SettingKeyCreativeModelSettings])
	result.CreativeWorkerCount = ParseCreativeWorkerCount(settings[SettingKeyCreativeWorkerCount])
	result.CreativeEnabled = settings[SettingKeyCreativeEnabled] != "false"

	return result
}
