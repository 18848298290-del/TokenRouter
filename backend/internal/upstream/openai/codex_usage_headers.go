package openai

import (
	"net/http"
	"strconv"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// codex_usage_headers.go parses Codex quota headers into independently present
// usage observations.
// Codex rate-limit headers expose primary and secondary windows before the
// quota layer assigns daily or weekly meaning. Each successfully parsed field
// remains independently observable, so a partial header set can still update
// quota information while missing or malformed values remain absent.

// ParseCodexRateLimitHeaders returns observed quota fields and a local RFC3339
// update timestamp. Nil or entirely unparseable headers return nil; percentage
// and integer fields are parsed independently, and this parser does not
// validate their business ranges.
func ParseCodexRateLimitHeaders(headers http.Header) *openai.OpenAICodexUsageSnapshot {
	snapshot := &openai.OpenAICodexUsageSnapshot{}
	hasData := false

	// A malformed percentage is left unobserved so other valid headers can still
	// update the quota snapshot without synthesizing a zero-percent measurement.
	parseFloat := func(key string) *float64 {
		if v := headers.Get(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return &f
			}
		}
		return nil
	}

	// Window durations and reset offsets are parsed independently, retaining
	// partial observations when a percentage or another integer field is missing.
	parseInt := func(key string) *int {
		if v := headers.Get(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return &i
			}
		}
		return nil
	}

	// Keep primary-window labels as observed; the quota normalizer uses the
	// window length to assign daily or weekly meaning after this header snapshot.
	if v := parseFloat("x-codex-primary-used-percent"); v != nil {
		snapshot.PrimaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-reset-after-seconds"); v != nil {
		snapshot.PrimaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-window-minutes"); v != nil {
		snapshot.PrimaryWindowMinutes = v
		hasData = true
	}

	// The secondary window is recorded independently.
	if v := parseFloat("x-codex-secondary-used-percent"); v != nil {
		snapshot.SecondaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-reset-after-seconds"); v != nil {
		snapshot.SecondaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-window-minutes"); v != nil {
		snapshot.SecondaryWindowMinutes = v
		hasData = true
	}

	// Percentage ratio between the primary and secondary limits.
	if v := parseFloat("x-codex-primary-over-secondary-limit-percent"); v != nil {
		snapshot.PrimaryOverSecondaryPercent = v
		hasData = true
	}

	if !hasData {
		return nil
	}

	snapshot.UpdatedAt = time.Now().Format(time.RFC3339)
	return snapshot
}
