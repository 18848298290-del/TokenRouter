package anthropic

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FableWindowReason identifies a persisted exhausted Fable window.
const FableWindowReason = "anthropic_7d_oi_window_exhausted"

// RateLimitReset contains reset times observed in provider headers.
type RateLimitReset struct {
	ResetAt       time.Time  // Selected rate-limit reset time.
	FiveHourReset *time.Time // Five-hour reset, or nil when absent.
}

type WindowLimit struct {
	Window        string
	ResetAt       time.Time
	FiveHourReset *time.Time
	Reason        string
}

func SelectExhaustedWindow(headers http.Header, now time.Time) *WindowLimit {
	reset5h, ok5hReset := parseWindowReset(headers, "5h", now)
	reset7d, ok7dReset := parseWindowReset(headers, "7d", now)

	exceeded5h := isFiveHourRejected(headers) || isWindowExceeded(headers, "5h")
	exceeded7d := isWindowExceeded(headers, "7d")

	if exceeded7d && ok7dReset {
		limit := &WindowLimit{
			Window:  "7d",
			ResetAt: reset7d,
			Reason:  "anthropic_7d_window_exhausted",
		}
		if ok5hReset {
			limit.FiveHourReset = &reset5h
		}
		return limit
	}
	if exceeded5h && ok5hReset {
		return &WindowLimit{
			Window:        "5h",
			ResetAt:       reset5h,
			FiveHourReset: &reset5h,
			Reason:        "anthropic_5h_window_exhausted",
		}
	}
	return nil
}

func isFiveHourRejected(headers http.Header) bool {
	return isWindowRejected(headers, "5h")
}

func isWindowRejected(headers http.Header, window string) bool {
	return strings.EqualFold(strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-"+window+"-status")), "rejected")
}

func parseWindowReset(headers http.Header, window string, now time.Time) (time.Time, bool) {
	maxAge := 8 * 24 * time.Hour
	if window == "5h" {
		maxAge = 6 * time.Hour
	}
	return parseResetTimestamp(headers.Get("anthropic-ratelimit-unified-"+window+"-reset"), now, maxAge)
}

// parseResetTimestamp accepts Unix seconds or milliseconds within (now, now+maxAge].
func parseResetTimestamp(raw string, now time.Time, maxAge time.Duration) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	if ts > 1e11 {
		ts /= 1000
	}
	resetAt := time.Unix(ts, 0)
	if !resetAt.After(now) || resetAt.After(now.Add(maxAge)) {
		return time.Time{}, false
	}
	return resetAt, true
}

// SelectFableWindowLimit returns an exhausted 7d_oi window when rejection or
// utilization headers indicate exhaustion. Missing or invalid window reset times
// use the aggregate reset; an unusable aggregate reset yields nil.
func SelectFableWindowLimit(headers http.Header, now time.Time) *WindowLimit {
	if !isWindowRejected(headers, "7d_oi") && !isWindowExceeded(headers, "7d_oi") {
		return nil
	}
	resetAt, ok := parseWindowReset(headers, "7d_oi", now)
	if !ok {
		resetAt, ok = parseAggregateReset(headers, now)
	}
	if !ok {
		return nil
	}
	return &WindowLimit{
		Window:  "7d_oi",
		ResetAt: resetAt,
		Reason:  FableWindowReason,
	}
}

// parseAggregateReset accepts aggregate reset times up to eight days ahead.
func parseAggregateReset(headers http.Header, now time.Time) (time.Time, bool) {
	return parseResetTimestamp(headers.Get("anthropic-ratelimit-unified-reset"), now, 8*24*time.Hour)
}

// CalculateRateLimitReset selects a reset from the 5h/7d Unix-second headers.
// A nil result lets the caller consider an aggregate reset header.
func CalculateRateLimitReset(headers http.Header, info func(string, ...any)) *RateLimitReset {
	reset5hStr := headers.Get("anthropic-ratelimit-unified-5h-reset")
	reset7dStr := headers.Get("anthropic-ratelimit-unified-7d-reset")

	if reset5hStr == "" && reset7dStr == "" {
		return nil
	}

	var reset5h, reset7d *time.Time
	if ts, err := strconv.ParseInt(reset5hStr, 10, 64); err == nil {
		t := time.Unix(ts, 0)
		reset5h = &t
	}
	if ts, err := strconv.ParseInt(reset7dStr, 10, 64); err == nil {
		t := time.Unix(ts, 0)
		reset7d = &t
	}

	is5hExceeded := isWindowExceeded(headers, "5h")
	is7dExceeded := isWindowExceeded(headers, "7d")

	if info != nil {
		info("anthropic_429_window_analysis",
			"is_5h_exceeded", is5hExceeded,
			"is_7d_exceeded", is7dExceeded,
			"reset_5h", reset5hStr,
			"reset_7d", reset7dStr,
		)
	}
	var chosen *time.Time
	switch {
	case is5hExceeded && is7dExceeded:
		// When both windows are exhausted, prefer seven days if available.
		chosen = reset7d
		if chosen == nil {
			chosen = reset5h
		}
	case is5hExceeded:
		chosen = reset5h
	case is7dExceeded:
		chosen = reset7d
	default:
		// Without an exhaustion signal, use the earlier available reset.
		chosen = pickSooner(reset5h, reset7d)
	}

	if chosen == nil {
		return nil
	}
	return &RateLimitReset{ResetAt: *chosen, FiveHourReset: reset5h}
}

// isWindowExceeded accepts surpassed-threshold=true or utilization within 1e-9 of one or higher.
func isWindowExceeded(headers http.Header, window string) bool {
	prefix := "anthropic-ratelimit-unified-" + window + "-"

	if st := headers.Get(prefix + "surpassed-threshold"); strings.EqualFold(st, "true") {
		return true
	}

	if utilStr := headers.Get(prefix + "utilization"); utilStr != "" {
		if util, err := strconv.ParseFloat(utilStr, 64); err == nil && util >= 1.0-1e-9 {
			// Allow rounding error near full utilization.
			return true
		}
	}

	return false
}

// pickSooner returns the earlier non-nil time, or nil when both are absent.
func pickSooner(a, b *time.Time) *time.Time {
	switch {
	case a != nil && b != nil:
		if a.Before(*b) {
			return a
		}
		return b
	case a != nil:
		return a
	default:
		return b
	}
}
