package anthropic

import (
	"net/http"
	"strconv"
)

// PassiveUsageFields extracts utilization and reset observations for a caller to
// merge into provider extra. Missing or invalid values are omitted.
func PassiveUsageFields(headers http.Header) map[string]any {
	extraUpdates := make(map[string]any, 6)
	// Five-hour utilization is a fraction, used for setup-token estimates.
	if utilStr := headers.Get("anthropic-ratelimit-unified-5h-utilization"); utilStr != "" {
		if util, err := strconv.ParseFloat(utilStr, 64); err == nil {
			extraUpdates["session_window_utilization"] = util
		}
	}
	// Seven-day utilization is a fraction.
	if utilStr := headers.Get("anthropic-ratelimit-unified-7d-utilization"); utilStr != "" {
		if util, err := strconv.ParseFloat(utilStr, 64); err == nil {
			extraUpdates["passive_usage_7d_utilization"] = util
		}
	}
	// Normalize seconds or milliseconds to Unix seconds.
	if resetStr := headers.Get("anthropic-ratelimit-unified-7d-reset"); resetStr != "" {
		if ts, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			if ts > 1e11 {
				ts /= 1000
			}
			extraUpdates["passive_usage_7d_reset"] = ts
		}
	}
	// Fable-specific seven-day utilization is a fraction.
	if utilStr := headers.Get("anthropic-ratelimit-unified-7d_oi-utilization"); utilStr != "" {
		if util, err := strconv.ParseFloat(utilStr, 64); err == nil {
			extraUpdates["passive_usage_7d_oi_utilization"] = util
		}
	}
	// Normalize seconds or milliseconds to Unix seconds.
	if resetStr := headers.Get("anthropic-ratelimit-unified-7d_oi-reset"); resetStr != "" {
		if ts, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			if ts > 1e11 {
				ts /= 1000
			}
			extraUpdates["passive_usage_7d_oi_reset"] = ts
		}
	}
	return extraUpdates
}
