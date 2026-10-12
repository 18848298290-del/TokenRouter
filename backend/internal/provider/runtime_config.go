package provider

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

const (
	WindowCostSchedulable    = billing.WindowCostSchedulable
	WindowCostStickyOnly     = billing.WindowCostStickyOnly
	WindowCostNotSchedulable = billing.WindowCostNotSchedulable
)

// RuntimeConfig supplies provider configuration and window data for rule evaluation.
// Callers provide scheduling metadata rather than authentication credentials.
type RuntimeConfig struct {
	Extra              map[string]any `json:"-"`
	Concurrency        int
	SessionWindowStart *time.Time
	SessionWindowEnd   *time.Time
}

// WindowCostSchedulability is the spending-window scheduling state.
type WindowCostSchedulability = billing.WindowCostSchedulability

// SanitizeManagedBaseRPM normalizes base_rpm in the supplied Extra map.
// Values are clamped to 0 through 10000; a nil map or missing key is unchanged.
func SanitizeManagedBaseRPM(extra map[string]any) {
	if extra == nil {
		return
	}
	raw, ok := extra["base_rpm"]
	if !ok {
		return
	}
	v := ParseExtraInt(raw)
	if v < 0 {
		v = 0
	} else if v > 10000 {
		v = 10000
	}
	extra["base_rpm"] = v
}

// GetMaxSessions returns the maximum concurrent session count.
// Zero disables the session limit.
func (a *RuntimeConfig) GetMaxSessions() int {
	if a.Extra == nil {
		return 0
	}
	if v, ok := a.Extra["max_sessions"]; ok {
		return ParseExtraInt(v)
	}
	return 0
}

// GetSessionIdleTimeoutMinutes returns the session idle timeout in minutes.
// Missing or nonpositive values default to five minutes.
func (a *RuntimeConfig) GetSessionIdleTimeoutMinutes() int {
	if a.Extra == nil {
		return 5
	}
	if v, ok := a.Extra["session_idle_timeout_minutes"]; ok {
		val := ParseExtraInt(v)
		if val > 0 {
			return val
		}
	}
	return 5
}

// GetBaseRPM returns the base requests-per-minute limit.
// Zero disables the limit; missing, invalid, and negative values return zero.
func (a *RuntimeConfig) GetBaseRPM() int {
	if a.Extra == nil {
		return 0
	}
	if v, ok := a.Extra["base_rpm"]; ok {
		val := ParseExtraInt(v)
		if val > 0 {
			return val
		}
	}
	return 0
}

// GetRPMStrategy returns the request-rate scheduling strategy.
// "tiered" uses three admission levels by default; "sticky_exempt" exempts sticky requests above the base limit.
func (a *RuntimeConfig) GetRPMStrategy() string {
	if a.Extra == nil {
		return "tiered"
	}
	if v, ok := a.Extra["rpm_strategy"]; ok {
		if s, ok := v.(string); ok && s == "sticky_exempt" {
			return "sticky_exempt"
		}
	}
	return "tiered"
}

// GetRPMStickyBuffer returns the extra request capacity reserved for sticky sessions.
// A positive override wins; otherwise concurrency plus maxSessions covers active requests and retained sessions.
// With a positive base RPM, the computed buffer is at least max(baseRPM/5, 1); otherwise it is zero.
func (a *RuntimeConfig) GetRPMStickyBuffer() int {
	if a.Extra == nil {
		return 0
	}
	override := 0
	if v, ok := a.Extra["rpm_sticky_buffer"]; ok {
		override = ParseExtraInt(v)
	}
	return policy.RPMStickyBuffer(a.GetBaseRPM(), a.Concurrency, a.GetMaxSessions(), override)
}

// CheckRPMSchedulability evaluates the current request-per-minute count.
// The result is Schedulable, StickyOnly, or NotSchedulable.
func (a *RuntimeConfig) CheckRPMSchedulability(current int) WindowCostSchedulability {
	return WindowCostSchedulability(policy.CheckRPM(current, a.GetBaseRPM(), a.GetRPMStickyBuffer(), a.GetRPMStrategy()))
}

// GetWindowCostLimit returns the five-hour spending threshold in USD.
// Zero disables the spending limit.
func (a *RuntimeConfig) GetWindowCostLimit() float64 {
	if a.Extra == nil {
		return 0
	}
	if v, ok := a.Extra["window_cost_limit"]; ok {
		return ParseExtraFloat64(v)
	}
	return 0
}

// GetWindowCostStickyReserve returns the spending reserve for sticky sessions in USD.
// Missing or nonpositive values default to 10 USD.
func (a *RuntimeConfig) GetWindowCostStickyReserve() float64 {
	if a.Extra == nil {
		return 10.0
	}
	if v, ok := a.Extra["window_cost_sticky_reserve"]; ok {
		val := ParseExtraFloat64(v)
		if val > 0 {
			return val
		}
	}
	return 10.0
}

// GetCurrentWindowStartTime returns the active spending window start.
// The stored window is used when both timestamps are present and its end is in the future.
// Otherwise, the predicted window starts at the current hour
// in the supplied time's location.
func (a *RuntimeConfig) GetCurrentWindowStartTime(now time.Time) time.Time {
	return billing.CurrentCostWindowStart(a.SessionWindowStart, a.SessionWindowEnd, now)
}

// ParseExtraFloat64 parses a numeric Extra value, returning zero for unsupported or invalid input.
func ParseExtraFloat64(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return 0
}

func ParseExtraTime(value any) time.Time {
	if s, ok := value.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func ParseExtraInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return 0
}

// GetUserMsgQueueMode returns the provider message queue mode.
// "serialize" queues requests, "throttle" applies soft rate limiting, and empty uses the global setting.
func (a *RuntimeConfig) GetUserMsgQueueMode() string {
	if a.Extra == nil {
		return ""
	}
	// Prefer user_msg_queue_mode; an unsupported nonempty mode uses the global setting.
	if mode, ok := a.Extra["user_msg_queue_mode"].(string); ok && mode != "" {
		if mode == "serialize" || mode == "throttle" {
			return mode
		}
		return "" // An invalid mode uses global configuration.
	}
	// The legacy user_msg_queue_enabled flag selects serialize when true.
	if enabled, ok := a.Extra["user_msg_queue_enabled"].(bool); ok && enabled {
		return "serialize"
	}
	return ""
}
