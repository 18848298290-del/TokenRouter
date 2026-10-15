package ops

import (
	"strconv"
	"strings"

	settingvalues "github.com/TokenFlux/TokenRouter/internal/settings"
)

// admin_settings_read.go decodes stored monitoring preferences and quota
// thresholds for administrator settings responses.
// Administrator settings show stored Ops preferences even when deployment
// options separately restrict execution. Monitoring switches default to
// enabled, the metrics cadence uses the collector's accepted range, and
// advanced settings supply normalized OpenAI quota thresholds.

// AdminReadSettings is the Ops portion of an administrator settings response.
type AdminReadSettings struct {
	// OpenAIQuotaAutoPauseSettings contains normalized provider quota thresholds.
	OpenAIQuotaAutoPauseSettings OpsOpenAIProviderQuotaAutoPauseSettings
	// OpsMetricsIntervalSeconds is the interval between metrics samples, in
	// seconds.
	OpsMetricsIntervalSeconds int
	// OpsMonitoringEnabled is the stored preference for overall Ops monitoring.
	OpsMonitoringEnabled bool
	// OpsRealtimeMonitoringEnabled is the stored preference for realtime Ops
	// monitoring.
	OpsRealtimeMonitoringEnabled bool
}

// ReadAdminSettings converts stored values into administrator monitoring
// preferences and quota thresholds. Missing switches are enabled, malformed
// intervals use sixty seconds, and valid intervals are clamped to sixty through
// thirty-six hundred seconds. Deployment availability is assessed by the
// settings response's caller.
func ReadAdminSettings(settings map[string]string) *AdminReadSettings {
	result := &AdminReadSettings{}

	result.OpsMonitoringEnabled = !settingvalues.IsExplicitFalse(settings[SettingKeyOpsMonitoringEnabled])
	result.OpsRealtimeMonitoringEnabled = !settingvalues.IsExplicitFalse(settings[SettingKeyOpsRealtimeMonitoringEnabled])
	result.OpsMetricsIntervalSeconds = 60
	if raw := strings.TrimSpace(settings[SettingKeyOpsMetricsIntervalSeconds]); raw != "" {
		// Keep the displayed cadence within the same minute-to-hour range accepted by
		// the collector.
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
