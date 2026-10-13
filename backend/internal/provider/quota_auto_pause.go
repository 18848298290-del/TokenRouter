package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OpenAI quota pauses compare Codex window percentages with provider-specific
// ratios or inherited defaults. Each window can be disabled independently,
// and a positive five-hour match takes precedence over a seven-day match. A
// reached reset or an observation at least two hours old removes that window
// from the pause decision.
//
// Stored Extra values can arrive as native numbers, JSON numbers, or strings.
// These readers retain the supported numeric value before applying scale and
// threshold rules. Ordered clamping bounds ordinary values but leaves NaN
// unchanged, so callers cannot treat parsing success as finite-value
// validation. Missing or malformed observation times do not establish
// staleness; absolute reset times take precedence over relative seconds
// anchored to the observation time.

// CodexAutoPauseStaleAfter sets the observation-age cutoff beyond which quota
// usage no longer contributes to automatic provider pauses.
const CodexAutoPauseStaleAfter = 2 * time.Hour

// QuotaAutoPauseSettings supplies inherited OpenAI window ratios when a
// provider override is absent or nonpositive.
type QuotaAutoPauseSettings struct {
	// DefaultThreshold5h and DefaultThreshold7d are ratios for distinct usage
	// windows. Provider settings inherit them independently, so one default can
	// remain active when the other window's default is zero.
	DefaultThreshold5h float64 `json:"default_threshold_5h"` // five-hour utilization ratio
	DefaultThreshold7d float64 `json:"default_threshold_7d"` // seven-day utilization ratio
}

// QuotaAutoPauseDecision identifies the first enabled window whose
// utilization reaches its effective ratio threshold.
type QuotaAutoPauseDecision struct {
	Window string // matched Codex window name
	// Threshold and Utilization describe Window in the same fractional units.
	// EvaluateQuotaAutoPause fills both from the selected qualifying check so
	// callers can explain that window's observed match.
	Threshold, Utilization float64 // effective threshold ratio and observed usage ratio
}

// quotaClamp bounds values using ordered comparisons against zero and one.
// Negative values and negative infinity become zero, values above one become
// one, and NaN passes through because neither comparison matches.
func quotaClamp(value float64) float64 {
	// EvaluateQuotaAutoPause requires a positive threshold, so a retained NaN
	// cannot select a pause window in that consumer.
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// ResolveProviderExtraBool reads a flag from supported bool, string, or
// numeric Extra representations. Strings use trimmed ParseBool syntax;
// nonzero native float32, float64, int, and int64 values are true. A
// json.Number must be accepted by Int64, while missing, nil, unsupported, and
// failed parsing values are false.
func ResolveProviderExtraBool(extra map[string]any, key string) bool {
	if len(extra) == 0 {
		return false
	}
	value, ok := extra[key]
	if !ok || value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		// An invalid stored flag selects false, leaving inherited thresholds
		// eligible rather than applying an unrecognized per-window exemption.
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && parsed
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		// Numeric flags use integer syntax; a decimal JSON number such as 1.0
		// cannot establish this boolean exemption and therefore stays false.
		if i, err := v.Int64(); err == nil {
			return i != 0
		}
	}
	return false
}

// ResolveProviderExtraNumber returns the first successfully read numeric
// value in key order. Missing, nil, unsupported, and unparseable entries
// leave later keys eligible; supported strings are trimmed, and parsing
// success does not reject NaN or infinity.
func ResolveProviderExtraNumber(extra map[string]any, keys ...string) (float64, bool) {
	if len(extra) == 0 {
		return 0, false
	}
	for _, key := range keys {
		value, ok := extra[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case float64:
			return v, true
		case float32:
			return float64(v), true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case json.Number:
			// Parsing failure leaves later aliases eligible, so one unusable numeric
			// representation does not hide another supplied threshold.
			parsed, err := v.Float64()
			if err == nil {
				return parsed, true
			}
		case string:
			// A malformed string leaves subsequent aliases eligible under the same
			// first-usable-value rule used for the other numeric inputs.
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

// ResolveOpenAIQuotaUtilization returns a Codex window ratio when its
// percentage survives the reset and observation-age checks. Missing or
// nonpositive percentages return false, as do reached resets and observations
// classified as stale by the shared helpers.
//
// The same positive usage can therefore stop contributing after its reset or
// the staleness cutoff. Missing observation times leave the age check
// undecided, so reset handling still determines eligibility. Parsing retains
// nonfinite values; EvaluateQuotaAutoPause's later ratio comparison decides
// whether the returned value reaches a positive threshold.
func ResolveOpenAIQuotaUtilization(extra map[string]any, window string, now time.Time) (float64, bool) {
	usedPercent := ReadOpenAIQuotaUsedPercent(extra, window)
	if usedPercent <= 0 {
		return 0, false
	}
	if OpenAIQuotaWindowReset(extra, window, now) {
		return 0, false
	}
	// Ignore a known-old observation so the threshold cannot exclude a provider
	// using usage sampled at least two hours before this check.
	if OpenAICodexSnapshotStaleForPause(extra, now) {
		return 0, false
	}
	// Codex stores percentage points; pause thresholds use fractional ratios.
	return usedPercent / 100, true
}

// OpenAICodexSnapshotStaleForPause reports whether a parsed observation time
// is at least two hours before now. Missing or malformed timestamps return
// false, and future timestamps remain below the age cutoff.
func OpenAICodexSnapshotStaleForPause(extra map[string]any, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return false
	}
	// An unparseable observation supplies no age evidence, so this helper leaves
	// quota enforcement eligible instead of classifying the snapshot stale.
	updatedAt, err := ParseUsageTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= CodexAutoPauseStaleAfter
}

// OpenAIQuotaWindowReset reports whether the selected window's reset has been
// reached. A parsable absolute reset wins immediately, including a future
// reset; otherwise positive relative seconds are added to a parsed
// observation time or to now when that time is unavailable.
//
// Callers supply seconds whose duration conversion fits. With a representable
// positive duration, the fallback to now keeps relative-only data future for
// this check. Nonpositive relative seconds provide no reset. This precedence
// lets an available absolute representation decide before a relative one is
// considered.
func OpenAIQuotaWindowReset(extra map[string]any, window string, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	if resetAtRaw, ok := extra["codex_"+window+"_reset_at"]; ok {
		// A usable absolute deadline decides this check immediately. Failed parsing
		// leaves relative seconds available to describe the same window.
		if resetAt, err := ParseUsageTime(fmt.Sprint(resetAtRaw)); err == nil {
			return !now.Before(resetAt)
		}
	}
	resetAfter := ParseExtraInt(extra["codex_"+window+"_reset_after_seconds"])
	if resetAfter <= 0 {
		return false
	}
	base := now
	if updatedRaw, ok := extra["codex_usage_updated_at"]; ok {
		if updatedAt, err := ParseUsageTime(fmt.Sprint(updatedRaw)); err == nil {
			base = updatedAt
		}
	}
	// Relative seconds need an observation anchor to become an elapsed deadline.
	// A representable positive duration stays future when based on now.
	resetAt := base.Add(time.Duration(resetAfter) * time.Second)
	return !now.Before(resetAt)
}

// ReadOpenAIQuotaUsedPercent reads the selected Codex window in percentage
// points, returning zero when unavailable. A value of one means one percent;
// the helper passes supported parsed numbers through without finite-value
// validation.
func ReadOpenAIQuotaUsedPercent(extra map[string]any, window string) float64 {
	if len(extra) == 0 {
		return 0
	}
	if value, ok := ResolveProviderExtraNumber(extra, "codex_"+window+"_used_percent"); ok {
		return value
	}
	return 0
}

// ResolveQuotaAutoPauseThresholds returns the effective five-hour and
// seven-day ratio limits in that order. Each parsed provider value is bounded
// with quotaClamp; a nonpositive result independently inherits the
// corresponding bounded default.
//
// Positive overrides can replace either default, and zero defaults leave
// their windows inactive. NaN remains in its window because ordered
// comparisons neither bound it nor select inheritance. EvaluateQuotaAutoPause
// later requires a positive threshold before considering a window, so NaN
// does not select that window for a pause.
func ResolveQuotaAutoPauseThresholds(extra map[string]any, settings QuotaAutoPauseSettings) (float64, float64) {
	threshold5h, _ := ResolveProviderExtraNumber(extra, "auto_pause_5h_threshold")
	threshold7d, _ := ResolveProviderExtraNumber(extra, "auto_pause_7d_threshold")
	threshold5h = quotaClamp(threshold5h)
	threshold7d = quotaClamp(threshold7d)
	if threshold5h > 0 && threshold7d > 0 {
		return threshold5h, threshold7d
	}
	if threshold5h <= 0 {
		threshold5h = quotaClamp(settings.DefaultThreshold5h)
	}
	if threshold7d <= 0 {
		threshold7d = quotaClamp(settings.DefaultThreshold7d)
	}
	return threshold5h, threshold7d
}

// EvaluateQuotaAutoPause returns the first enabled OpenAI window whose usable
// ratio reaches its effective threshold. Other platforms return no decision,
// and each window's disable flag overrides both its provider ratio and
// inherited default.
//
// The five-hour window is checked before the seven-day window; their ordering
// determines the reported decision when both qualify. Nonpositive thresholds
// skip their windows, and reset or stale observations provide no match. A
// true result describes a calculated pause condition for the caller to
// display or apply.
func EvaluateQuotaAutoPause(platform string, extra map[string]any, settings QuotaAutoPauseSettings, now time.Time) (bool, QuotaAutoPauseDecision) {
	if platform != PlatformOpenAI {
		return false, QuotaAutoPauseDecision{}
	}
	// Disable flags are evaluated separately from inherited ratios so an
	// administrator can exempt one window while the other still enforces its
	// provider override or process-cached default.
	disabled5h := ResolveProviderExtraBool(extra, "auto_pause_5h_disabled")
	disabled7d := ResolveProviderExtraBool(extra, "auto_pause_7d_disabled")
	threshold5h, threshold7d := ResolveQuotaAutoPauseThresholds(extra, settings)
	if !disabled5h && threshold5h > 0 {
		if utilization, ok := ResolveOpenAIQuotaUtilization(extra, "5h", now); ok && utilization >= threshold5h {
			return true, QuotaAutoPauseDecision{Window: "5h", Threshold: threshold5h, Utilization: utilization}
		}
	}
	if !disabled7d && threshold7d > 0 {
		if utilization, ok := ResolveOpenAIQuotaUtilization(extra, "7d", now); ok && utilization >= threshold7d {
			return true, QuotaAutoPauseDecision{Window: "7d", Threshold: threshold7d, Utilization: utilization}
		}
	}
	return false, QuotaAutoPauseDecision{}
}
