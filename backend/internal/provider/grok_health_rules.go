package provider

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// grok_health_rules.go calculates Grok rate-limit deadlines and scheduler
// utilization from quota observations.
// Grok rate limits combine Retry-After with request and token window resets. A
// parseable observation time fixes the Retry-After deadline across repeated
// reads, while exhausted windows can supply a later deadline. An expired
// Retry-After suppresses a fresh default pause when no future window remains,
// so rereading an old snapshot does not perpetually renew that particular
// cooldown.
//
// Provider history can extend a new OAuth 429 after a recent completed
// cooldown, and normalization retains a longer stored deadline. Scheduling
// utilization selects the more heavily used valid window, keeping request order
// on ties. Its reset is capped separately for scheduling display fields; that
// cap does not change the rate-limit deadline calculation.

// Grok cooldown durations govern missing reset evidence, repeated OAuth 429s,
// and the maximum reset horizon exported to scheduling metadata.
const (
	grokRateLimitFallbackCooldown    = 2 * time.Minute
	grokRateLimitRepeatCooldown      = 10 * time.Minute
	grokRateLimitSustainedCooldown   = 30 * time.Minute
	grokRateLimitMaxAdaptiveCooldown = time.Hour
	grokRateLimitBackoffQuietPeriod  = time.Hour
	grokMaxSchedulingResetHorizon    = 25 * time.Hour
)

// NormalizeGrokExhaustedWindowResets repairs reset representations for
// exhausted request and token windows in place. A nil snapshot or a supplied
// deadline that is not future leaves all windows unchanged. A positive
// ResetUnix takes precedence over ResetAt; a missing, malformed, or expired
// selected reset uses resetAt. Both Unix seconds and UTC RFC3339 text then
// describe the same chosen instant. Callers keep the snapshot stable during
// mutation.
func NormalizeGrokExhaustedWindowResets(snapshot *usageview.QuotaSnapshot, resetAt, now time.Time) {
	if snapshot == nil || !resetAt.After(now) {
		return
	}
	// Keep Unix seconds and RFC3339 text synchronized when an exhausted
	// window
	// needs a replacement deadline; consumers read both representations on
	// different paths.
	for _, window := range []*usageview.QuotaWindow{snapshot.Requests, snapshot.Tokens} {
		if window == nil || window.Remaining == nil || *window.Remaining > 0 {
			continue
		}
		candidate := time.Time{}
		if window.ResetUnix != nil && *window.ResetUnix > 0 {
			candidate = time.Unix(*window.ResetUnix, 0)
		} else if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(window.ResetAt)); err == nil {
			candidate = parsed
		}
		if !candidate.After(now) {
			candidate = resetAt
		}
		resetUnix := candidate.Unix()
		window.ResetUnix = &resetUnix
		window.ResetAt = candidate.UTC().Format(time.RFC3339)
	}
}

// GrokRateLimitResetAt returns the latest future deadline from positive
// Retry-After and exhausted request/token windows, or false when the
// observation supplies no current pause. Retry-After uses parsed UpdatedAt when
// available and otherwise starts at now; window resets prefer positive Unix
// seconds over RFC3339 text.
//
// If that Retry-After has expired and no future window reset exists, the result
// stays unpaused even when stale exhaustion or status 429 remains. Otherwise
// exhaustion or 429 without usable deadlines selects the two-minute default.
// This distinction prevents a stored expired retry observation from renewing
// its own fallback pause on every read.
func GrokRateLimitResetAt(snapshot *usageview.QuotaSnapshot, now time.Time) (time.Time, bool) {
	if snapshot == nil {
		return time.Time{}, false
	}

	// Anchor Retry-After to a parseable observation time so repeated reads
	// share its deadline; absent or invalid timestamp text uses this call's
	// now.
	retryAfterExpired := false
	var resetAt time.Time
	if snapshot.RetryAfterSeconds != nil && *snapshot.RetryAfterSeconds > 0 {
		observedAt := now
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(snapshot.UpdatedAt)); err == nil {
			observedAt = parsed
		}
		retryAfterResetAt := observedAt.Add(time.Duration(*snapshot.RetryAfterSeconds) * time.Second)
		if retryAfterResetAt.After(now) {
			resetAt = retryAfterResetAt
		} else {
			retryAfterExpired = true
		}
	}

	exhausted := false
	// Positive Unix reset evidence wins over text for each exhausted
	// window.
	// Choose the latest future deadline so one reset cannot release another
	// still-exhausted window early.
	for _, window := range []*usageview.QuotaWindow{snapshot.Requests, snapshot.Tokens} {
		if window == nil || window.Remaining == nil || *window.Remaining > 0 {
			continue
		}
		exhausted = true
		candidate := time.Time{}
		if window.ResetUnix != nil && *window.ResetUnix > 0 {
			candidate = time.Unix(*window.ResetUnix, 0)
		} else if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(window.ResetAt)); err == nil {
			candidate = parsed
		}
		if candidate.After(now) && candidate.After(resetAt) {
			resetAt = candidate
		}
	}
	if !resetAt.IsZero() {
		return resetAt, true
	}
	// A later window reset was already considered above. An expired
	// Retry-After must not
	// create another cooldown merely because this snapshot still reports
	// exhaustion.
	if retryAfterExpired {
		return time.Time{}, false
	}
	// Exhaustion or 429 still needs a retry deadline when usable reset
	// evidence
	// is absent; the standard cooldown supplies that bounded initial wait.
	if exhausted || snapshot.StatusCode == 429 {
		return now.Add(grokRateLimitFallbackCooldown), true
	}
	return time.Time{}, false
}

// GrokRateLimitResetAtForProvider extends the snapshot deadline using recent
// OAuth 429 history when the previous cooldown has ended. Other provider types,
// statuses, absent history, still-active prior limits, and limits older than
// the quiet period keep the snapshot result.
//
// A positive previous cooldown selects ten minutes, thirty minutes, or one hour
// according to its duration. The later of that adaptive deadline and the
// snapshot deadline wins, so repeated failures can lengthen a pause while
// upstream evidence can still request a longer wait. This function returns the
// decision for separate persistence.
func GrokRateLimitResetAtForProvider(provider *Record, snapshot *usageview.QuotaSnapshot, now time.Time) (time.Time, bool) {
	resetAt, limited := GrokRateLimitResetAt(snapshot, now)
	if !limited || (provider == nil || !provider.IsGrokOAuth()) || snapshot == nil || snapshot.StatusCode != 429 {
		return resetAt, limited
	}
	if provider.RateLimitedAt == nil || provider.RateLimitResetAt == nil {
		return resetAt, true
	}
	previousResetAt := *provider.RateLimitResetAt
	if previousResetAt.After(now) || now.Sub(previousResetAt) > grokRateLimitBackoffQuietPeriod {
		return resetAt, true
	}
	previousCooldown := previousResetAt.Sub(*provider.RateLimitedAt)
	if previousCooldown <= 0 {
		return resetAt, true
	}

	// The previous pause has ended recently, so its observed duration
	// chooses
	// the next escalation tier instead of restarting every 429 at the
	// default.
	adaptiveCooldown := grokRateLimitRepeatCooldown
	switch {
	case previousCooldown >= grokRateLimitSustainedCooldown:
		adaptiveCooldown = grokRateLimitMaxAdaptiveCooldown
	case previousCooldown >= grokRateLimitRepeatCooldown:
		adaptiveCooldown = grokRateLimitSustainedCooldown
	}
	adaptiveResetAt := now.Add(adaptiveCooldown)
	if adaptiveResetAt.After(resetAt) {
		resetAt = adaptiveResetAt
	}
	return resetAt, true
}

// NormalizeGrokRateLimitResetAt returns a future deadline while retaining any
// later provider deadline. A supplied expired or zero instant becomes the
// two-minute default relative to now. A provider with a later RateLimitResetAt
// then extends that result, so delayed persistence cannot shorten the pause
// already present in the supplied record. The record is read as given;
// comparison against a concurrently updated stored row belongs to the writer.
func NormalizeGrokRateLimitResetAt(provider *Record, resetAt, now time.Time) time.Time {
	// The observed deadline has elapsed, so give persistence a future
	// baseline
	// before retaining any longer pause already present in the provider
	// record.
	if !resetAt.After(now) {
		resetAt = now.Add(grokRateLimitFallbackCooldown)
	}
	if provider != nil && provider.RateLimitResetAt != nil && provider.RateLimitResetAt.After(resetAt) {
		resetAt = *provider.RateLimitResetAt
	}
	return resetAt
}

// IsSuccessfulGrokRateLimitRecovery reports whether a Grok OAuth row has both
// limit timestamps and a 2xx observation. The recovery writer separately
// compares those timestamps before clearing stored state.
func IsSuccessfulGrokRateLimitRecovery(provider *Record, snapshot *usageview.QuotaSnapshot) bool {
	return (provider != nil && provider.IsGrokOAuth()) &&
		provider.RateLimitedAt != nil &&
		provider.RateLimitResetAt != nil &&
		snapshot != nil &&
		snapshot.StatusCode >= 200 &&
		snapshot.StatusCode < 300
}

// BuildGrokSchedulerExtraUpdates returns scheduling utilization and a fresh
// observation timestamp from a usable request or token window. Missing or
// wholly unusable snapshots return nil for the caller's existing metadata
// handling.
//
// The most utilized valid window supplies its optional Unix reset. Future
// resets are capped at the scheduling horizon, while absent or past resets
// contribute no reset key. The cap bounds the scheduling field independently
// from provider-wide rate-limit persistence, and the returned patch leaves
// deletion or retention of an existing omitted key to its consumer.
func BuildGrokSchedulerExtraUpdates(snapshot *usageview.QuotaSnapshot) map[string]any {
	if snapshot == nil {
		return nil
	}
	util, reset, ok := GrokSnapshotUtilization(snapshot)
	if !ok {
		return nil
	}
	updates := map[string]any{
		"grok_sched_utilization":      util,
		"grok_sched_usage_updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if reset != nil {
		// grokThresholdCandidates reads this reset with the selected
		// utilization.
		// Bound that scheduling metadata independently from the
		// provider-wide
		// rate-limit deadline; omit elapsed resets and cap future ones
		// at the horizon.
		now := time.Now()
		if reset.After(now) {
			capped := *reset
			if horizon := now.Add(grokMaxSchedulingResetHorizon); capped.After(horizon) {
				capped = horizon
			}
			updates["grok_sched_reset_at"] = capped.UTC().Format(time.RFC3339)
		}
	}
	return updates
}

// GrokSnapshotUtilization returns the highest usable request or token
// utilization percentage and that window's optional Unix reset. A window needs
// a positive limit and a remaining counter; no usable window returns false.
// Negative remaining values count as exhausted, and remaining above limit
// clamps utilization to zero. Requests are considered first and win equal
// utilization. The winning window can return a reset before now or a nil reset
// because freshness is evaluated separately by consumers.
func GrokSnapshotUtilization(snapshot *usageview.QuotaSnapshot) (float64, *time.Time, bool) {
	if snapshot == nil {
		return 0, nil, false
	}
	best := -1.0
	var bestReset *time.Time
	consider := func(window *usageview.QuotaWindow) {
		if window == nil || window.Limit == nil || *window.Limit <= 0 || window.Remaining == nil {
			return
		}
		// Counter excursions stay within the display's
		// zero-to-one-hundred range.
		remaining := max(*window.Remaining, 0)
		util := (1 - float64(remaining)/float64(*window.Limit)) * 100
		if util < 0 {
			util = 0
		}
		if util > 100 {
			util = 100
		}
		if util > best {
			best = util
			if window.ResetUnix != nil {
				t := time.Unix(*window.ResetUnix, 0).UTC()
				bestReset = &t
			} else {
				bestReset = nil
			}
		}
	}
	consider(snapshot.Requests)
	consider(snapshot.Tokens)
	if best < 0 {
		return 0, nil, false
	}
	return best, bestReset, true
}
