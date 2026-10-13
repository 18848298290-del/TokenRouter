package anthropic

import (
	"net/http"
	"strconv"
)

// Anthropic response headers supply passive utilization fractions and reset
// timestamps for provider observations. The parser returns recognized values
// under Extra field names, omits parse failures, and converts large integer
// timestamps from milliseconds to seconds before the caller merges them.

// PassiveUsageFields returns parsed utilization fractions and reset values
// under provider Extra field names. Missing or malformed values are omitted;
// large reset integers are divided into Unix seconds, while other numeric
// values pass through without range checks.
func PassiveUsageFields(headers http.Header) map[string]any {
	// Retain each independently parseable observation and omit failures, allowing
	// the provider merge to leave unrelated previously stored Extra values
	// available when one Header is malformed.
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
			// BuildPassiveUsageWindow consumes stored resets as Unix seconds. Reduce
			// millisecond Header values before writing the corresponding Extra field.
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
			// BuildPassiveUsageWindow consumes stored resets as Unix seconds. Reduce
			// millisecond Header values before writing the corresponding Extra field.
			if ts > 1e11 {
				ts /= 1000
			}
			extraUpdates["passive_usage_7d_oi_reset"] = ts
		}
	}
	return extraUpdates
}
