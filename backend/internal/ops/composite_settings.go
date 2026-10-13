package ops

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Combined settings requests store monitoring switches as scalar strings while
// quota thresholds live inside the advanced-settings JSON. Preparing those
// values preserves the interval's nonpositive omission rule. Replacing quota
// thresholds first decodes the enclosing settings object so its other supported
// fields remain available after normalization.
//
// Administrator and scheduler reads handle decoding failures differently.
// Administrator reads return zero quota thresholds for a failed decode. Runtime
// reads retain the initialized structure and any field assignments made before
// a decode error, and normalize only successful input. Runtime reads therefore
// can expose accepted quota fields from an object whose other fields failed
// decoding, so the scheduler can receive a different value from the
// administrator settings view.

// CompositeMonitoringSettings carries the scalar Ops values decoded from a
// combined settings request.
type CompositeMonitoringSettings struct {
	// OpsMonitoringEnabled supplies the combined request monitoring preference.
	OpsMonitoringEnabled bool `json:"ops_monitoring_enabled"`
	// OpsRealtimeMonitoringEnabled supplies the combined request realtime
	// preference.
	OpsRealtimeMonitoringEnabled bool `json:"ops_realtime_monitoring_enabled"`
	// OpsMetricsIntervalSeconds supplies the requested collector interval, in
	// seconds.
	OpsMetricsIntervalSeconds int `json:"ops_metrics_interval_seconds"`
}

// PrepareMonitoringSettings formats both switches and any positive metrics
// interval for persistence. Nonpositive intervals produce no interval entry;
// SettingsParticipant subsequently removes switch entries whose input fields
// were omitted.
func PrepareMonitoringSettings(v CompositeMonitoringSettings) map[string]string {
	values := map[string]string{"ops_monitoring_enabled": strconv.FormatBool(v.OpsMonitoringEnabled), "ops_realtime_monitoring_enabled": strconv.FormatBool(v.OpsRealtimeMonitoringEnabled)}
	if v.OpsMetricsIntervalSeconds > 0 {
		values["ops_metrics_interval_seconds"] = strconv.Itoa(v.OpsMetricsIntervalSeconds)
	}
	return values
}

// MergeQuotaAutoPauseSettings replaces quota thresholds within decoded advanced
// settings and normalizes the combined value. Blank input starts from defaults,
// while decoding failures return an error before producing a replacement.
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

// ParseQuotaAutoPauseSettings returns normalized quota thresholds from
// advanced-settings JSON. Blank input uses defaults; a decoding error discards
// the partially decoded value and returns zero thresholds for the administrator
// settings view.
func ParseQuotaAutoPauseSettings(raw string) OpsOpenAIProviderQuotaAutoPauseSettings {
	cfg := defaultOpsAdvancedSettings()
	if strings.TrimSpace(raw) != "" {
		// Administrator reads treat failed decoding as unusable configuration, even
		// when the decoder assigned some fields before returning its error.
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			return OpsOpenAIProviderQuotaAutoPauseSettings{}
		}
	}
	normalizeOpsAdvancedSettings(cfg)
	return cfg.OpenAIProviderQuotaAutoPause
}

// ParseRuntimeQuotaAutoPauseSettings reads quota thresholds using the
// scheduler's runtime parsing policy. It starts from advanced-setting defaults
// and normalizes thresholds after a successful decode. Blank input returns the
// initialized quota values directly.
//
// A failed decode can already have assigned valid fields before reporting an
// incompatible value. Those assignments remain in the returned quota structure
// and bypass normalization, so this path can differ from the administrator
// parser's zero result for invalid input.
func ParseRuntimeQuotaAutoPauseSettings(raw string) OpsOpenAIProviderQuotaAutoPauseSettings {
	cfg := defaultOpsAdvancedSettings()
	if strings.TrimSpace(raw) != "" {
		// Runtime quota consumers receive the decoder output on failure; threshold
		// clamping applies only after the whole advanced-settings value decoded
		// successfully.
		if jsonErr := json.Unmarshal([]byte(raw), cfg); jsonErr == nil {
			normalizeOpsAdvancedSettings(cfg)
		}
	}
	return cfg.OpenAIProviderQuotaAutoPause
}
