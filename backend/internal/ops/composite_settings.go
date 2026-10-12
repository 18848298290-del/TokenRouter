package ops

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CompositeMonitoringSettings exposes monitoring toggles and the metrics interval in composite settings.
type CompositeMonitoringSettings struct {
	OpsMonitoringEnabled         bool `json:"ops_monitoring_enabled"`
	OpsRealtimeMonitoringEnabled bool `json:"ops_realtime_monitoring_enabled"`
	OpsMetricsIntervalSeconds    int  `json:"ops_metrics_interval_seconds"`
}

// PrepareMonitoringSettings omits nonpositive metrics intervals from the values to persist.
func PrepareMonitoringSettings(v CompositeMonitoringSettings) map[string]string {
	values := map[string]string{"ops_monitoring_enabled": strconv.FormatBool(v.OpsMonitoringEnabled), "ops_realtime_monitoring_enabled": strconv.FormatBool(v.OpsRealtimeMonitoringEnabled)}
	if v.OpsMetricsIntervalSeconds > 0 {
		values["ops_metrics_interval_seconds"] = strconv.Itoa(v.OpsMetricsIntervalSeconds)
	}
	return values
}

// MergeQuotaAutoPauseSettings replaces quota auto-pause settings and normalizes the combined Ops configuration.
func MergeQuotaAutoPauseSettings(raw string, quota OpsOpenAIProviderQuotaAutoPauseSettings) (*OpsAdvancedSettings, error) {
	value := defaultOpsAdvancedSettings()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), value); err != nil {
			return nil, fmt.Errorf("unmarshal ops advanced settings: %w", err)
		}
	}
	value.OpenAIProviderQuotaAutoPause = quota
	normalizeOpsAdvancedSettings(value)
	return value, nil
}

// ParseQuotaAutoPauseSettings parses provider quota auto-pause settings, returning zero values on invalid JSON.
func ParseQuotaAutoPauseSettings(raw string) OpsOpenAIProviderQuotaAutoPauseSettings {
	cfg := defaultOpsAdvancedSettings()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			return OpsOpenAIProviderQuotaAutoPauseSettings{}
		}
	}
	normalizeOpsAdvancedSettings(cfg)
	return cfg.OpenAIProviderQuotaAutoPause
}

// ParseRuntimeQuotaAutoPauseSettings starts with defaults and normalizes only successfully decoded JSON.
func ParseRuntimeQuotaAutoPauseSettings(raw string) OpsOpenAIProviderQuotaAutoPauseSettings {
	cfg := defaultOpsAdvancedSettings()
	if strings.TrimSpace(raw) != "" {
		if jsonErr := json.Unmarshal([]byte(raw), cfg); jsonErr == nil {
			normalizeOpsAdvancedSettings(cfg)
		}
	}
	return cfg.OpenAIProviderQuotaAutoPause
}
