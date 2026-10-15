package anthropic

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// rate_limit_observation.go selects Anthropic quota cooldown endpoints from
// reset times and exhaustion observations.
// Anthropic quota signals can describe a five-hour window, a seven-day window,
// or a Fable-specific weekly window. Selection keeps the five-hour reset
// separately when a weekly limit controls cooldown, so session accounting can
// retain its shorter endpoint.
//
// Successful-response window checks require a future reset within the relevant
// window horizon and accept seconds or milliseconds. The older 429 calculation
// instead parses window resets directly as Unix seconds and chooses among them
// from exhaustion flags, retaining its different validation behavior. Fable
// selection can use the aggregate reset when its own reset is unusable. These
// parsers return observations; provider health code decides persistence, model
// scope, and recovery.

// FableWindowReason identifies exhausted model-specific weekly quota
// observations passed to provider health for persistence and recovery
// decisions.
const FableWindowReason = "anthropic_7d_oi_window_exhausted"

// RateLimitReset separates selected cooldown timing from five-hour session
// timing.
type RateLimitReset struct {
	ResetAt time.Time // selected cooldown endpoint
	// FiveHourReset retains a parsed session endpoint even when ResetAt follows a
	// weekly cooldown; nil means no five-hour reset was parsed.
	FiveHourReset *time.Time // optional session endpoint
}

// WindowLimit identifies an exhausted quota window and its usable reset.
type WindowLimit struct {
	Window  string    // selected quota label
	ResetAt time.Time // validated reset endpoint
	// FiveHourReset carries a valid short-window reset alongside provider
	// exhaustion; Fable-only results leave it nil because their window is
	// model-specific.
	FiveHourReset *time.Time // optional session endpoint
	Reason        string     // persisted exhaustion reason
}

// SelectExhaustedWindow returns a usable exhausted five-hour or seven-day
// observation at now. Rejected status contributes only to the five-hour branch,
// while surpassed-threshold and utilization can mark either window exhausted.
//
// A valid exhausted weekly window takes precedence and retains any valid
// five-hour reset separately. If its reset is unusable, a valid exhausted
// five-hour window can still win. Per-window resets need to lie within the
// future horizon before selection; no eligible window returns nil for the
// provider health caller.
func SelectExhaustedWindow(headers http.Header, now time.Time) *WindowLimit {
	reset5h, ok5hReset := parseWindowReset(headers, "5h", now)
	reset7d, ok7dReset := parseWindowReset(headers, "7d", now)

	exceeded5h := isFiveHourRejected(headers) || isWindowExceeded(headers, "5h")
	exceeded7d := isWindowExceeded(headers, "7d")

	// A usable exhausted weekly window takes precedence over a usable session
	// window, while carrying the shorter reset separately for session accounting
	// in the health caller.
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

// isFiveHourRejected reports whether the five-hour Header status is rejected.
func isFiveHourRejected(headers http.Header) bool {
	return isWindowRejected(headers, "5h")
}

// isWindowRejected reports a trimmed, case-insensitive rejected status for
// window.
func isWindowRejected(headers http.Header, window string) bool {
	return strings.EqualFold(strings.TrimSpace(headers.Get("anthropic-ratelimit-unified-"+window+"-status")), "rejected")
}

// parseWindowReset parses a window's reset using its future horizon. The
// five-hour window permits six hours ahead, while other windows permit eight
// days; invalid or out-of-range reset values return false.
func parseWindowReset(headers http.Header, window string, now time.Time) (time.Time, bool) {
	// The default horizon accepts one day beyond a seven-day window.
	maxAge := 8 * 24 * time.Hour
	if window == "5h" {
		// Five-hour validation accepts one additional hour beyond that window.
		maxAge = 6 * time.Hour
	}
	return parseResetTimestamp(headers.Get("anthropic-ratelimit-unified-"+window+"-reset"), now, maxAge)
}

// parseResetTimestamp accepts trimmed integer timestamps within (now,
// now+maxAge]. Integers above the milliseconds threshold are reduced to
// seconds, and malformed, expired, or too-distant values return a zero time and
// false.
func parseResetTimestamp(raw string, now time.Time, maxAge time.Duration) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	// Values classified as milliseconds would otherwise enter validation as
	// far-future seconds; reduce their precision before comparing the accepted
	// reset horizon.
	if ts > 1e11 {
		ts /= 1000
	}
	resetAt := time.Unix(ts, 0)
	if !resetAt.After(now) || resetAt.After(now.Add(maxAge)) {
		return time.Time{}, false
	}
	return resetAt, true
}

// SelectFableWindowLimit returns a Fable weekly limit when rejection status or
// utilization indicates exhaustion. Its own future reset is preferred; a
// missing or unusable value tries the aggregate reset under the same eight-day
// horizon.
//
// No exhaustion or no usable reset returns nil, letting the provider health
// caller leave that window unobserved. The result names 7d_oi and carries
// FableWindowReason but has no five-hour reset, because this model-specific
// window is separate from session accounting.
func SelectFableWindowLimit(headers http.Header, now time.Time) *WindowLimit {
	if !isWindowRejected(headers, "7d_oi") && !isWindowExceeded(headers, "7d_oi") {
		return nil
	}
	resetAt, ok := parseWindowReset(headers, "7d_oi", now)
	// An exhausted Fable signal can lack a usable dedicated reset. Try the
	// aggregate endpoint under the same future horizon before withholding the
	// observation.
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

// parseAggregateReset reads the aggregate reset with an eight-day future
// horizon. It delegates seconds or milliseconds conversion and rejects
// malformed, expired, and more distant values through parseResetTimestamp.
func parseAggregateReset(headers http.Header, now time.Time) (time.Time, bool) {
	return parseResetTimestamp(headers.Get("anthropic-ratelimit-unified-reset"), now, 8*24*time.Hour)
}

// CalculateRateLimitReset selects a cooldown observation from five-hour and
// seven-day Headers and retains the parsed five-hour reset. It reads integer
// Unix seconds directly, without the future-horizon or milliseconds validation
// used by successful-response window selection.
//
// When both windows are exceeded, weekly reset wins if parsed, otherwise
// five-hour reset is used. A single exceeded window selects its own reset, even
// when that yields nil; without exhaustion, the earlier parsed reset wins.
// Optional logging reports the analyzed signals, and nil lets the caller
// consider its aggregate Header.
func CalculateRateLimitReset(headers http.Header, info func(string, ...any)) *RateLimitReset {
	reset5hStr := headers.Get("anthropic-ratelimit-unified-5h-reset")
	reset7dStr := headers.Get("anthropic-ratelimit-unified-7d-reset")

	if reset5hStr == "" && reset7dStr == "" {
		return nil
	}

	var reset5h, reset7d *time.Time
	// Unparseable per-window timestamps remain absent observations. Selection can
	// still use the other parsed window, or return nil for the caller's aggregate
	// reset handling.
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
		// Weekly exhaustion can outlast the session reset, so select its parsed
		// endpoint first and retain the five-hour value separately for provider
		// session handling.
		chosen = reset7d
		if chosen == nil {
			chosen = reset5h
		}
	case is5hExceeded:
		chosen = reset5h
	case is7dExceeded:
		chosen = reset7d
	default:
		// Without an exhausted-window signal, the earlier parsed endpoint limits the
		// selected cooldown while the full five-hour observation remains available in
		// the result.
		chosen = pickSooner(reset5h, reset7d)
	}

	if chosen == nil {
		return nil
	}
	return &RateLimitReset{ResetAt: *chosen, FiveHourReset: reset5h}
}

// isWindowExceeded reports surpassed-threshold=true or utilization at least one
// minus the rounding tolerance. Threshold text is case-insensitive but
// untrimmed, and missing or malformed utilization leaves that numeric test
// false.
func isWindowExceeded(headers http.Header, window string) bool {
	prefix := "anthropic-ratelimit-unified-" + window + "-"

	if st := headers.Get(prefix + "surpassed-threshold"); strings.EqualFold(st, "true") {
		return true
	}

	if utilStr := headers.Get(prefix + "utilization"); utilStr != "" {
		if util, err := strconv.ParseFloat(utilStr, 64); err == nil && util >= 1.0-1e-9 {
			// Numeric exhaustion still applies when surpassed-threshold is absent or false.
			return true
		}
	}

	return false
}

// pickSooner returns the earlier available time pointer, preferring b on
// equality. A missing pointer selects the other, both missing yields nil, and
// the selected pointer remains shared with its caller.
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
