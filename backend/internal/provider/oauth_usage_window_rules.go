package provider

import (
	"encoding/json"
	"fmt"
	"time"
)

// Usage-window display combines sampled fractions, absolute resets, relative
// resets, and local statistics from different provider contracts. Passive
// Extra converts fractions to percentage without clearing expired utilization,
// Codex Extra clears used percentage after reset, and Anthropic keeps supplied
// percentage even when parsing reset text fails.
//
// Codex timing prefers a valid absolute reset over a positive relative
// duration, whose base is stored observation time when parseable and the
// supplied fallback time otherwise. Countdown uses the clock callback, while
// utilization expiry uses the separately supplied time. Setup-token estimates
// instead clear reset/countdown on expiry and retain an empty FiveHour when no
// session exists. Each constructor preserves its source-specific absence rules
// rather than inferring an upstream window from local display statistics.

// OAuth usage durations bound API reuse, retry admission, statistics reuse,
// and rolling free-plan display windows in their respective provider consumers.
const (
	// OAuthUsageAPICacheTTL bounds reuse of successful API observations.
	OAuthUsageAPICacheTTL = 3 * time.Minute
	// OAuthUsageAPIErrorCacheTTL bounds reuse of failed API observations.
	OAuthUsageAPIErrorCacheTTL = 1 * time.Minute
	// OAuthUsageAntigravityErrorTTL bounds reuse of recoverable Antigravity
	// failures.
	OAuthUsageAntigravityErrorTTL = 1 * time.Minute
	// OAuthUsageAPIQueryMaxJitter supplies the nanosecond bound passed to
	// Anthropic-query jitter; callback results are not clamped by the dispatcher.
	OAuthUsageAPIQueryMaxJitter = 800 * time.Millisecond
	// OAuthUsageWindowStatsCacheTTL bounds reuse of local window statistics.
	OAuthUsageWindowStatsCacheTTL = 1 * time.Minute
	// OAuthUsageOpenAIProbeCacheTTL sets the OpenAI/Grok observation age bound.
	OAuthUsageOpenAIProbeCacheTTL = 10 * time.Minute
	// OAuthUsageGrokProbeRetryTTL sets ordinary Grok probe retry spacing.
	OAuthUsageGrokProbeRetryTTL = 1 * time.Minute
	// OAuthUsageGrokFreeQuotaWindow sets free-plan rolling statistics duration.
	OAuthUsageGrokFreeQuotaWindow = 24 * time.Hour
)

// BuildPassiveUsageWindow converts sampled Extra into a percentage and
// optional Unix-second reset. Both nonpositive parsed observations produce
// nil; reached now is required and caller-owned Extra stays stable during
// conversion.
//
// Utilization alone and reset alone both retain a window, while malformed
// values parse to zero and special floating values are not filtered here. A
// positive reset is converted directly to int64 Unix seconds; finite
// representable input loses fractional precision. Countdown is clamped to
// zero after expiry. Sampled utilization remains unchanged after that reset,
// so this observation differs from Codex expired-window display.
func BuildPassiveUsageWindow(extra map[string]any, utilKey, resetKey string, now func() time.Time) *UsageProgress {
	util := ParseExtraFloat64(extra[utilKey])
	resetRaw := ParseExtraFloat64(extra[resetKey])
	if util <= 0 && resetRaw <= 0 {
		return nil
	}
	var resetAt *time.Time
	var remaining int
	// Treat positive reset observations as Unix seconds while retaining sampled
	// utilization independently. Finite representable timestamps lose subsecond
	// precision; nonfinite values receive the same unchecked conversion. Expired
	// countdowns cannot become negative in this passive display.
	if resetRaw > 0 {
		t := time.Unix(int64(resetRaw), 0)
		resetAt = &t
		remaining = max(int(t.Sub(now()).Seconds()), 0)
	}
	// Convert sampled fraction to percent without imposing quota bounds.
	return &UsageProgress{
		Utilization:      util * 100,
		ResetsAt:         resetAt,
		RemainingSeconds: remaining,
	}
}

// ApplyExtraToUsage mutates the supplied usage with available Codex windows
// from stable Extra. Nil usage skips work; an absent replacement preserves the
// existing window, and callers synchronize shared display mutations and supply
// the reached clock.
func ApplyExtraToUsage(usage *UsageInfo, extra map[string]any, now time.Time, clock func() time.Time) {
	if usage == nil {
		return
	}
	if progress := BuildCodexUsageProgressFromExtra(extra, "5h", now, clock); progress != nil {
		usage.FiveHour = progress
	}
	if progress := BuildCodexUsageProgressFromExtra(extra, "7d", now, clock); progress != nil {
		usage.SevenDay = progress
	}
}

// BuildCodexUsageProgressFromExtra builds a supported 5h or 7d window when its
// used-percentage key is present. Empty Extra, unsupported window, or absent
// key returns nil; a present unusable percentage parses to zero.
//
// Valid absolute reset text wins over positive relative seconds. Relative
// timing uses valid codex_usage_updated_at or supplied now, so stored
// observations do not renew merely because display is rebuilt. Countdown uses
// required clock and clamps negative seconds, while expiry clears utilization
// using the separate now argument and retains the reset pointer. Callers keep
// Extra stable and choose both time sources consistently when their display
// requires matching decisions.
func BuildCodexUsageProgressFromExtra(extra map[string]any, window string, now time.Time, clock func() time.Time) *UsageProgress {
	if len(extra) == 0 {
		return nil
	}

	var (
		usedPercentKey string
		resetAfterKey  string
		resetAtKey     string
	)

	switch window {
	case "5h":
		usedPercentKey = "codex_5h_used_percent"
		resetAfterKey = "codex_5h_reset_after_seconds"
		resetAtKey = "codex_5h_reset_at"
	case "7d":
		usedPercentKey = "codex_7d_used_percent"
		resetAfterKey = "codex_7d_reset_after_seconds"
		resetAtKey = "codex_7d_reset_at"
	default:
		return nil
	}

	usedRaw, ok := extra[usedPercentKey]
	if !ok {
		return nil
	}

	progress := &UsageProgress{Utilization: ParseExtraFloat64(usedRaw)}
	// Prefer absolute timing so a later display rebuild cannot extend a known
	// reset. Failed parsing leaves relative timing available rather than
	// rejecting the independently present usage percentage.
	if resetAtRaw, ok := extra[resetAtKey]; ok {
		if resetAt, err := ParseUsageTime(fmt.Sprint(resetAtRaw)); err == nil {
			progress.ResetsAt = &resetAt
			// Whole-second countdown clamps expiry without changing the reset pointer.
			progress.RemainingSeconds = max(int(resetAt.Sub(clock()).Seconds()), 0)
		}
	}
	// Reconstruct missing absolute timing from the observation base plus positive
	// relative seconds. An unusable stored observation timestamp falls back to
	// supplied now, retaining utilization when neither timing form is usable.
	if progress.ResetsAt == nil {
		if resetAfterSeconds := ParseExtraInt(extra[resetAfterKey]); resetAfterSeconds > 0 {
			base := now
			if updatedAtRaw, ok := extra["codex_usage_updated_at"]; ok {
				if updatedAt, err := ParseUsageTime(fmt.Sprint(updatedAtRaw)); err == nil {
					base = updatedAt
				}
			}
			resetAt := base.Add(time.Duration(resetAfterSeconds) * time.Second)
			progress.ResetsAt = &resetAt
			// Whole-second countdown clamps expiry without changing the reset pointer.
			progress.RemainingSeconds = max(int(resetAt.Sub(clock()).Seconds()), 0)
		}
	}

	// Clear expired utilization against supplied now while retaining the observed
	// reset pointer. Countdown uses clock separately, so callers choose time
	// sources consistently when they require those display decisions to agree.
	if progress.ResetsAt != nil && !now.Before(*progress.ResetsAt) {
		progress.Utilization = 0
	}

	return progress
}

// CodexWindowStatsStart chooses a local statistics start from an observed
// future reset or supplied rolling duration. Callers provide the duration
// associated with the selected upstream window.
//
// An active reset anchors the start at reset minus duration, so statistics
// follow that observed cycle even when the resulting start is later or earlier
// than now minus duration. Missing, nil, equal, or expired reset uses the
// rolling start instead. The function does not validate positive duration or
// limit the resulting range; its statistics caller owns those inputs.
func CodexWindowStatsStart(progress *UsageProgress, fallbackWindow time.Duration, now time.Time) time.Time {
	if progress != nil && progress.ResetsAt != nil && now.Before(*progress.ResetsAt) {
		return progress.ResetsAt.Add(-fallbackWindow)
	}
	return now.Add(-fallbackWindow)
}

// ParseUsageTime parses supported RFC 3339 forms including fractional seconds.
// It tries the declared layouts in order without trimming, returning the first
// successful time or a diagnostic error containing the original input.
func ParseUsageTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	}
	// Try declared timestamp representations in order so stored/header samples
	// can share one display parser without normalizing or discarding their input
	// text before failure is diagnosed.
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time: %s", s)
}

// BuildUsageInfo converts a stable nonnil Anthropic usage response into
// display windows. UpdatedAt remains borrowed, and reached clock and
// parse-failure logf callbacks are required.
//
// FiveHour always exists, even without reset text; weekly, Sonnet, and
// overage-included Fable windows exist only when their reset text is nonempty.
// Invalid reset text keeps the supplied utilization with unavailable timing
// and is diagnosed rather than rejecting the entire response. Parsed
// countdowns retain signed whole seconds and do not clear utilization on
// expiry, allowing this view to reflect sampled upstream usage separately from
// the Codex reset rule.
func BuildUsageInfo(resp *ClaudeUsageResponse, updatedAt *time.Time, now func() time.Time, logf func(string, ...any)) *UsageInfo {
	info := &UsageInfo{
		UpdatedAt: updatedAt,
	}

	// Preserve FiveHour utilization even without usable reset timing. Parse
	// failures below are diagnosed while the window remains available, and parsed
	// countdowns retain signed whole seconds for the sampled upstream reset.
	info.FiveHour = &UsageProgress{
		Utilization: resp.FiveHour.Utilization,
	}
	if resp.FiveHour.ResetsAt != "" {
		if fiveHourReset, err := ParseUsageTime(resp.FiveHour.ResetsAt); err == nil {
			info.FiveHour.ResetsAt = &fiveHourReset
			info.FiveHour.RemainingSeconds = int(fiveHourReset.Sub(now()).Seconds())
		} else {
			logf("Failed to parse FiveHour.ResetsAt: %s, error: %v", resp.FiveHour.ResetsAt, err)
		}
	}

	// Admit the weekly view only with nonempty reset text, preserving absence for
	// responses without that observation. Failed parsing still exposes its usage
	// while diagnostics explain why timing is unavailable.
	if resp.SevenDay.ResetsAt != "" {
		if sevenDayReset, err := ParseUsageTime(resp.SevenDay.ResetsAt); err == nil {
			info.SevenDay = &UsageProgress{
				Utilization:      resp.SevenDay.Utilization,
				ResetsAt:         &sevenDayReset,
				RemainingSeconds: int(sevenDayReset.Sub(now()).Seconds()),
			}
		} else {
			logf("Failed to parse SevenDay.ResetsAt: %s, error: %v", resp.SevenDay.ResetsAt, err)
			info.SevenDay = &UsageProgress{
				Utilization: resp.SevenDay.Utilization,
			}
		}
	}

	// Retain Sonnet usage independently of the ordinary weekly window when its
	// reset text exists. Unusable timing keeps the specialized percentage visible
	// and emits its own parsing diagnostic.
	if resp.SevenDaySonnet.ResetsAt != "" {
		if sonnetReset, err := ParseUsageTime(resp.SevenDaySonnet.ResetsAt); err == nil {
			info.SevenDaySonnet = &UsageProgress{
				Utilization:      resp.SevenDaySonnet.Utilization,
				ResetsAt:         &sonnetReset,
				RemainingSeconds: int(sonnetReset.Sub(now()).Seconds()),
			}
		} else {
			logf("Failed to parse SevenDaySonnet.ResetsAt: %s, error: %v", resp.SevenDaySonnet.ResetsAt, err)
			info.SevenDaySonnet = &UsageProgress{
				Utilization: resp.SevenDaySonnet.Utilization,
			}
		}
	}

	// Map overage-included usage to Fable, the display field also used for 7d_oi
	// response-header observations. Nonempty invalid reset text retains that usage
	// with a diagnostic instead of removing the specialized window.
	if fable := resp.SevenDayOverageIncluded; fable.ResetsAt != "" {
		if fableReset, err := ParseUsageTime(fable.ResetsAt); err == nil {
			info.SevenDayFable = &UsageProgress{
				Utilization:      fable.Utilization,
				ResetsAt:         &fableReset,
				RemainingSeconds: int(fableReset.Sub(now()).Seconds()),
			}
		} else {
			logf("Failed to parse SevenDayFable.ResetsAt: %s, error: %v", fable.ResetsAt, err)
			info.SevenDayFable = &UsageProgress{
				Utilization: fable.Utilization,
			}
		}
	}

	return info
}

// EstimateSetupTokenUsage builds a FiveHour estimate from a stable nonnil
// provider and required clock. The session reset is copied independently;
// provider Extra remains borrowed during reads.
//
// Only float64 or parseable json.Number sampled fractions take precedence over
// status estimates. Missing or unusable samples select rejected as one
// hundred, allowed_warning as eighty, or zero otherwise. An expired session
// clears utilization, reset pointer, and countdown, while a missing session
// retains an empty FiveHour. Clock is consulted separately for countdown and
// expiry, and no seven-day observation is derived from session state.
func EstimateSetupTokenUsage(provider *Record, now func() time.Time) *UsageInfo {
	info := &UsageInfo{}

	if provider.SessionWindowEnd != nil {
		// Truncate countdown to whole seconds and clamp expired session display.
		remaining := max(int(provider.SessionWindowEnd.Sub(now()).Seconds()), 0)

		// Convert sampled fractions to percent before coarse status estimates so a
		// valid zero sample remains distinguishable from missing/unusable data.
		// Numeric parse failure leaves status fallback available.
		var utilization float64
		var found bool
		if stored, ok := provider.Extra["session_window_utilization"]; ok {
			switch v := stored.(type) {
			case float64:
				utilization = v * 100
				found = true
			case json.Number:
				if f, err := v.Float64(); err == nil {
					utilization = f * 100
					found = true
				}
			}
		}

		// Use status estimates only without a supported numeric sample, keeping
		// rejected/warning observations visible until the session timing expires.
		// Other statuses leave the estimate at zero.
		if !found {
			switch provider.SessionWindowStatus {
			case "rejected":
				utilization = 100.0
			case "allowed_warning":
				utilization = 80.0
			}
		}

		info.FiveHour = &UsageProgress{
			Utilization:      utilization,
			ResetsAt:         clonePointer(provider.SessionWindowEnd),
			RemainingSeconds: remaining,
		}

		// Clear expired session timing together with usage so a stale stored end
		// cannot retain a reset pointer while the estimate awaits another sample.
		if info.FiveHour.ResetsAt != nil && !now().Before(*info.FiveHour.ResetsAt) {
			info.FiveHour.Utilization = 0
			info.FiveHour.ResetsAt = nil
			info.FiveHour.RemainingSeconds = 0
		}
	} else {
		// Keep an empty five-hour object when no session window is known.
		info.FiveHour = &UsageProgress{
			Utilization:      0,
			RemainingSeconds: 0,
		}
	}

	// Session state does not supply a seven-day window.
	return info
}

// BuildGeminiUsageProgress returns nil for a nonpositive local request limit.
// A positive limit produces an unclamped used percentage, copied reset,
// nonnegative whole-second countdown, and supplied request/token/cost
// statistics without validating their signs.
func BuildGeminiUsageProgress(used, limit int64, resetAt time.Time, tokens int64, cost float64, now time.Time) *UsageProgress {
	// Nonpositive limits omit the local window; the caller owns limit policy.
	if limit <= 0 {
		return nil
	}
	// Convert request ratio to percent without clamping excess usage.
	utilization := (float64(used) / float64(limit)) * 100
	// Display whole remaining seconds without negative expiry values.
	remainingSeconds := max(int(resetAt.Sub(now).Seconds()), 0)
	resetCopy := resetAt
	return &UsageProgress{
		Utilization:      utilization,
		ResetsAt:         &resetCopy,
		RemainingSeconds: remainingSeconds,
		UsedRequests:     used,
		LimitRequests:    limit,
		WindowStats: &WindowStats{
			Requests: used,
			Tokens:   tokens,
			Cost:     cost,
		},
	}
}

// RecalcAntigravityRemainingSeconds mutates an existing FiveHour reset
// countdown through required reached now. Nil info or absent reset skips work;
// callers synchronize shared display mutation, and expired timing becomes zero
// without changing utilization.
func RecalcAntigravityRemainingSeconds(info *UsageInfo, now func() time.Time) {
	if info == nil {
		return
	}
	if info.FiveHour != nil && info.FiveHour.ResetsAt != nil {
		// Clamp expired cached countdown while preserving its sampled utilization.
		remaining := max(int(info.FiveHour.ResetsAt.Sub(now()).Seconds()), 0)
		info.FiveHour.RemainingSeconds = remaining
	}
}

// AntigravityCacheTTL chooses an observation lifetime for the usage cache. Nil
// observation uses the recoverable-error lifetime, and caller-owned status
// fields remain stable during classification.
//
// Forbidden state takes precedence over other error fields and uses the
// successful three-minute lifetime, retaining account recovery display without
// treating it as a transient failure. Other nonempty errors use one minute so
// the cache permits an earlier retry. A successful ordinary observation uses
// three minutes; this selection owns lifetime only and does not validate cache
// identity or freshness.
func AntigravityCacheTTL(info *UsageInfo) time.Duration {
	if info == nil {
		return OAuthUsageAntigravityErrorTTL
	}
	if info.IsForbidden {
		return OAuthUsageAPICacheTTL
	}
	if info.ErrorCode != "" || info.Error != "" {
		return OAuthUsageAntigravityErrorTTL
	}
	return OAuthUsageAPICacheTTL
}
