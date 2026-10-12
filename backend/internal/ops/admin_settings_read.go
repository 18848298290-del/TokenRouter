package ops

import (
	"strconv"
	"strings"

	settingvalues "github.com/TokenFlux/TokenRouter/internal/settings"
)

// AdminReadSettings contains monitoring and quota auto-pause settings for administrators.
type AdminReadSettings struct {
	OpenAIQuotaAutoPauseSettings OpsOpenAIProviderQuotaAutoPauseSettings
	OpsMetricsIntervalSeconds    int
	OpsMonitoringEnabled         bool
	OpsRealtimeMonitoringEnabled bool
}

// ReadAdminSettings parses monitoring and quota auto-pause settings from stored values.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	result.OpsMonitoringEnabled = !settingvalues.IsExplicitFalse(settings[SettingKeyOpsMonitoringEnabled])
	result.OpsRealtimeMonitoringEnabled = !settingvalues.IsExplicitFalse(settings[SettingKeyOpsRealtimeMonitoringEnabled])
	result.OpsMetricsIntervalSeconds = 60
	if raw := strings.TrimSpace(settings[SettingKeyOpsMetricsIntervalSeconds]); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			if v < 60 {
				v = 60
			}
			if v > 3600 {
				v = 3600
			}
			result.OpsMetricsIntervalSeconds = v
		}
	}
	result.OpenAIQuotaAutoPauseSettings = ParseQuotaAutoPauseSettings(settings[SettingKeyOpsAdvancedSettings])
	return result
}
