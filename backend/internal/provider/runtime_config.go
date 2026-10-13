package provider

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// Anthropic session, spending, and RPM eligibility read scalar settings from
// each provider's Extra map. Spending-window and RPM classifications share
// the billing allowance values, letting consumers distinguish ordinary
// admission from capacity reserved for existing sessions. The readers return
// parsed configuration for the scheduler and administrative display to apply.
//
// Extra contains both current queue modes and a legacy enable flag. A
// recognized current mode selects its policy, an invalid nonempty current
// mode falls back to global configuration, and only an absent or empty
// current mode permits the legacy flag to select serialization. Numeric
// readers also retain their own default rules: RPM activation requires a
// positive base, while reserve calculation can honor a positive override even
// with a disabled base.

// These billing allowance aliases give provider RPM callers the same
// ordinary, sticky-session, and blocked categories used for spending-window
// decisions.
const (
	WindowCostSchedulable    = billing.WindowCostSchedulable    // ordinary requests admitted
	WindowCostStickyOnly     = billing.WindowCostStickyOnly     // existing sessions admitted
	WindowCostNotSchedulable = billing.WindowCostNotSchedulable // all requests blocked
)

// RuntimeConfig borrows provider scheduling settings and recorded spending
// bounds for local policy readers. Keep Extra and pointed-to bounds stable
// while a reader evaluates them.
type RuntimeConfig struct {
	Extra       map[string]any `json:"-"` // borrowed provider scheduling settings
	Concurrency int            // configured provider concurrency capacity
	// SessionWindowStart and SessionWindowEnd supply the stored spending
	// interval. Billing retains the start only when both pointers exist and the
	// end is future relative to its supplied clock.
	SessionWindowStart *time.Time // recorded spending-window start instant
	SessionWindowEnd   *time.Time // recorded spending-window end instant
}

// WindowCostSchedulability aliases billing's ordinary, sticky-session, and
// blocked admission categories.
type WindowCostSchedulability = billing.WindowCostSchedulability

// SanitizeManagedBaseRPM replaces an existing base_rpm value with its parsed
// integer bounded to zero through ten thousand. Nil maps and absent keys stay
// unchanged; callers synchronize access because the supplied map is modified
// directly.
func SanitizeManagedBaseRPM(extra map[string]any) {
	if extra == nil {
		return
	}
	raw, ok := extra["base_rpm"]
	if !ok {
		return
	}
	// Managed provider writes bound this setting before storing Extra so later
	// rate eligibility receives the administration-supported RPM range.
	v := ParseExtraInt(raw)
	if v < 0 {
		v = 0
	} else if v > 10000 {
		v = 10000
	}
	extra["base_rpm"] = v
}

// GetMaxSessions returns the parsed max_sessions setting or zero when absent
// or unsupported. Selection and administrative consumers activate their
// session-count checks for positive limits; this reader passes negative
// parsed values through.
func (a *RuntimeConfig) GetMaxSessions() int {
	if a.Extra == nil {
		return 0
	}
	if v, ok := a.Extra["max_sessions"]; ok {
		return ParseExtraInt(v)
	}
	return 0
}

// GetSessionIdleTimeoutMinutes returns a positive parsed idle duration in
// minutes, defaulting to five. Positive values are unbounded here, so session
// consumers keep the minute-to-duration conversion representable before
// counting retained provider activity.
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

// GetBaseRPM returns a positive parsed requests-per-minute base or zero when
// absent, unsupported, or nonpositive. A zero result lets RPM consumers skip
// their observed-rate eligibility check.
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

// GetRPMStrategy selects sticky_exempt only for that exact stored string,
// defaulting to tiered. The exempt strategy keeps affinity requests eligible
// after the base rate is reached; tiered admission also applies the reserve
// ceiling.
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

// GetRPMStickyBuffer returns the RPM reserve calculated from provider
// capacity or a positive override. Nil Extra returns zero before policy
// evaluation; a positive override wins even with a disabled base. Otherwise a
// nonpositive base returns zero, and nonnegative capacity inputs are summed
// with a floor of one fifth of base or one request. Supply capacity inputs
// whose sum fits int because the delegated policy adds them directly.
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

// CheckRPMSchedulability classifies an observed request count using the
// provider's base, reserve, and strategy. Callers decide whether the request
// qualifies for sticky admission and record selected requests separately from
// this observation. Supply configuration whose base-plus-reserve sum fits int,
// as the delegated policy computes that ceiling directly.
func (a *RuntimeConfig) CheckRPMSchedulability(current int) WindowCostSchedulability {
	return WindowCostSchedulability(policy.CheckRPM(current, a.GetBaseRPM(), a.GetRPMStickyBuffer(), a.GetRPMStrategy()))
}

// GetWindowCostLimit returns the parsed local spending limit in USD or zero
// when unavailable. Scheduling and status readers enable their window-cost
// lookup when the result is positive; parsing does not establish finite-value
// validity.
func (a *RuntimeConfig) GetWindowCostLimit() float64 {
	if a.Extra == nil {
		return 0
	}
	if v, ok := a.Extra["window_cost_limit"]; ok {
		return ParseExtraFloat64(v)
	}
	return 0
}

// GetWindowCostStickyReserve returns a positive parsed sticky spending
// reserve in USD, defaulting to ten. Billing adds this reserve to the regular
// limit when classifying admission for sessions already bound to the
// provider.
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

// GetCurrentWindowStartTime returns the recorded start when both bounds exist
// and the end is future. Otherwise billing selects the current hour in now's
// location; a retained start passes through even when zero or future.
func (a *RuntimeConfig) GetCurrentWindowStartTime(now time.Time) time.Time {
	return billing.CurrentCostWindowStart(a.SessionWindowStart, a.SessionWindowEnd, now)
}

// ParseExtraFloat64 reads supported native numbers, JSON numbers, and trimmed
// floating-point strings, returning zero for unsupported or unparseable
// values. Successful parsing retains NaN and infinity, so consumers validate
// any finite-value requirement separately.
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
		// A failed numeric representation supplies zero so consumers can apply
		// their own disabled-limit or positive-reserve default rule.
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		// Malformed string settings supply zero for the same consumer-specific
		// activation and reserve-default decisions as unsupported numeric values.
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return 0
}

// ParseExtraTime returns a time from an RFC3339Nano or RFC3339 string, trying
// those layouts in order. Other types, whitespace-wrapped strings, and parse
// failures return the zero time for the caller's absent-value handling.
func ParseExtraTime(value any) time.Time {
	if s, ok := value.(string); ok {
		// Try both supported stored timestamp layouts before returning the
		// zero-time marker that callers use for an absent window observation.
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ParseExtraInt converts supported int, int64, float64, json.Number values
// accepted by Int64, and trimmed integer strings to int. In-range finite
// floats truncate fractional parts; unsupported or failed parsing returns
// zero, while native conversions perform no range validation.
func ParseExtraInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		// Whole-number settings discard an in-range float's fractional remainder.
		return int(v)
	case json.Number:
		// Integer syntax is required for whole-number settings; a failed
		// JSON-number parse becomes zero for the owning reader's default rule.
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	case string:
		// A malformed string cannot supply a whole-number setting, so zero lets the
		// owning getter select its disabled limit or positive default.
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return 0
}

// GetUserMsgQueueMode returns a recognized provider message queue mode or an
// empty string for global configuration. Serialize and throttle strings are
// accepted exactly; a nil Extra map also selects the global value.
//
// A nonempty current user_msg_queue_mode takes precedence over the legacy
// boolean, even when the current mode is invalid. Only an empty or
// unavailable string permits user_msg_queue_enabled=true to select serialize.
// This distinction prevents an explicitly invalid current choice from
// accidentally reviving an older enable flag.
func (a *RuntimeConfig) GetUserMsgQueueMode() string {
	if a.Extra == nil {
		return ""
	}
	// A nonempty current choice suppresses the legacy flag even when invalid,
	// keeping the current configuration's global fallback from reviving old
	// provider-level serialization.
	if mode, ok := a.Extra["user_msg_queue_mode"].(string); ok && mode != "" {
		if mode == "serialize" || mode == "throttle" {
			return mode
		}
		return ""
	}
	// Consult the legacy boolean after the current string is absent or empty so
	// persisted older provider settings can still request serialization.
	if enabled, ok := a.Extra["user_msg_queue_enabled"].(bool); ok && enabled {
		return "serialize"
	}
	return ""
}
