package provider

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	OAuthUsageAPICacheTTL         = 3 * time.Minute
	OAuthUsageAPIErrorCacheTTL    = 1 * time.Minute        // Error cache TTL, including 429 responses.
	OAuthUsageAntigravityErrorTTL = 1 * time.Minute        // Cache TTL for recoverable Antigravity errors.
	OAuthUsageAPIQueryMaxJitter   = 800 * time.Millisecond // Maximum query jitter.
	OAuthUsageWindowStatsCacheTTL = 1 * time.Minute
	OAuthUsageOpenAIProbeCacheTTL = 10 * time.Minute
	OAuthUsageGrokProbeRetryTTL   = 1 * time.Minute
	OAuthUsageGrokFreeQuotaWindow = 24 * time.Hour
)

// BuildPassiveUsageWindow reads fractional utilization and a Unix-second reset from Extra.
// It returns nil when both values are absent or nonpositive.
func BuildPassiveUsageWindow(extra map[string]any, utilKey, resetKey string, now func() time.Time) *UsageProgress {
	util := ParseExtraFloat64(extra[utilKey])
	resetRaw := ParseExtraFloat64(extra[resetKey])
	if util <= 0 && resetRaw <= 0 {
		return nil
	}
	var resetAt *time.Time
	var remaining int
	if resetRaw > 0 {
		t := time.Unix(int64(resetRaw), 0)
		resetAt = &t
		remaining = max(int(t.Sub(now()).Seconds()), 0)
	}
	return &UsageProgress{
		Utilization:      util * 100,
		ResetsAt:         resetAt,
		RemainingSeconds: remaining,
	}
}

// ApplyExtraToUsage updates Codex five-hour and seven-day windows from Extra.
// Existing windows remain when Extra cannot produce a replacement.
// Callers may use it before or after merging a fresh probe result.
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
	if resetAtRaw, ok := extra[resetAtKey]; ok {
		if resetAt, err := ParseUsageTime(fmt.Sprint(resetAtRaw)); err == nil {
			progress.ResetsAt = &resetAt
			progress.RemainingSeconds = max(int(resetAt.Sub(clock()).Seconds()), 0)
		}
	}
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
			progress.RemainingSeconds = max(int(resetAt.Sub(clock()).Seconds()), 0)
		}
	}

	// Expired windows display zero utilization after their reset time.
	if progress.ResetsAt != nil && !now.Before(*progress.ResetsAt) {
		progress.Utilization = 0
	}

	return progress
}

// CodexWindowStatsStart aligns local statistics with a future upstream reset, or uses a rolling window.
func CodexWindowStatsStart(progress *UsageProgress, fallbackWindow time.Duration, now time.Time) time.Time {
	if progress != nil && progress.ResetsAt != nil && now.Before(*progress.ResetsAt) {
		return progress.ResetsAt.Add(-fallbackWindow)
	}
	return now.Add(-fallbackWindow)
}

// ParseUsageTime parses supported RFC 3339 timestamp variants.
func ParseUsageTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse time: %s", s)
}

// BuildUsageInfo converts an Anthropic usage response and computes window countdowns.
func BuildUsageInfo(resp *ClaudeUsageResponse, updatedAt *time.Time, now func() time.Time, logf func(string, ...any)) *UsageInfo {
	info := &UsageInfo{
		UpdatedAt: updatedAt,
	}

	// Keep the five-hour object even when the upstream reset time is absent.
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

	// Include the seven-day window when a reset value is present.
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

	// Include the Sonnet window when a reset value is present.
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

	// Fable corresponds to the 7d_oi response-header window.
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

// EstimateSetupTokenUsage derives five-hour usage from stored session state.
func EstimateSetupTokenUsage(provider *Record, now func() time.Time) *UsageInfo {
	info := &UsageInfo{}

	if provider.SessionWindowEnd != nil {
		remaining := max(int(provider.SessionWindowEnd.Sub(now()).Seconds()), 0)

		// Prefer sampled fractional utilization, converting it to percent.
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

		// When utilization is missing, estimate it from the stored window status.
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

		// Clear utilization when the window has expired.
		// This reflects the reset while an active query has yet to update SessionWindowEnd.
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

func BuildGeminiUsageProgress(used, limit int64, resetAt time.Time, tokens int64, cost float64, now time.Time) *UsageProgress {
	// limit <= 0 means "no local quota window" (unknown or unlimited).
	if limit <= 0 {
		return nil
	}
	utilization := (float64(used) / float64(limit)) * 100
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

// RecalcAntigravityRemainingSeconds refreshes the five-hour countdown on a cached usage value.
// Expired countdowns are clamped to zero.
func RecalcAntigravityRemainingSeconds(info *UsageInfo, now func() time.Time) {
	if info == nil {
		return
	}
	if info.FiveHour != nil && info.FiveHour.ResetsAt != nil {
		remaining := max(int(info.FiveHour.ResetsAt.Sub(now()).Seconds()), 0)
		info.FiveHour.RemainingSeconds = remaining
	}
}

// AntigravityCacheTTL chooses the cache lifetime from the observation status.
// Success and forbidden results use three minutes.
// Other errors use one minute so recoverable failures are checked sooner.
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
