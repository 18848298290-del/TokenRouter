package provider

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// grok_quota_auto_pause.go selects automatic Grok scheduling pauses from
// snapshot age, Retry-After, and exhausted windows.
// Grok OAuth scheduling pauses use recent quota evidence, with Retry-After
// evaluated before request and token exhaustion. A parseable old observation is
// ignored after the shared stale interval, while missing or malformed
// observation time remains usable by this decision.
//
// Retry-After without a parseable timestamp stays active, whereas a valid
// timestamp produces a fixed expiry. Exhausted windows require a positive limit
// and a present remaining counter, and ignore an elapsed positive Unix reset.
// Their textual ResetAt is not read here. Request exhaustion wins when both windows
// qualify. Callers receive the chosen window and utilization for the existing
// scheduling decision, leaving provider state writes to their owner.

// EvaluateGrokQuotaAutoPause returns a scheduling pause and its first matching
// Grok OAuth quota decision. Nil, wrong provider kinds, invalid snapshot
// encoding, absent data, or a parseably stale observation returns no pause;
// clock is required once valid data reaches timing checks.
//
// Active Retry-After wins before requests and tokens, which are tested in that
// order. Missing or malformed observation timestamps remain usable, with
// Retry-After then treated as active. Window reset checks use positive Unix
// seconds, so textual resets alone do not release exhaustion. The returned
// utilization belongs to the selected rule and is not clamped for display.
func EvaluateGrokQuotaAutoPause(provider *Record, clock func() time.Time) (bool, QuotaAutoPauseDecision) {
	if provider == nil || !provider.IsGrok() || provider.Type != capability.ProviderTypeOAuth {
		return false, QuotaAutoPauseDecision{}
	}
	snapshot, err := GrokQuotaSnapshotFromExtra(provider.Extra)
	// Snapshot decoding failures supply no reliable scheduling evidence, so
	// leave this quota rule inactive and let other provider gates decide.
	if err != nil || snapshot == nil {
		return false, QuotaAutoPauseDecision{}
	}
	now := clock()
	if grokQuotaSnapshotStaleForPause(snapshot, now) {
		return false, QuotaAutoPauseDecision{}
	}
	if grokQuotaRetryAfterActive(snapshot, now) {
		return true, QuotaAutoPauseDecision{Window: "retry_after", Threshold: 1, Utilization: 1}
	}
	if paused, decision := GrokQuotaWindowAutoPause("requests", snapshot.Requests, now); paused {
		return true, decision
	}
	if paused, decision := GrokQuotaWindowAutoPause("tokens", snapshot.Tokens, now); paused {
		return true, decision
	}
	return false, QuotaAutoPauseDecision{}
}

// grokQuotaRetryAfterActive reports whether a positive Retry-After is still
// active at now. Missing or unparseable observation time returns true, while a
// valid time fixes expiry at that observation plus the delay.
func grokQuotaRetryAfterActive(snapshot *usageview.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || snapshot.RetryAfterSeconds == nil || *snapshot.RetryAfterSeconds <= 0 {
		return false
	}
	if strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return true
	}
	updatedAt, err := ParseUsageTime(snapshot.UpdatedAt)
	// An unparseable observation cannot establish that its retry delay has
	// elapsed; keep this pause active until a usable replacement is
	// observed.
	if err != nil {
		return true
	}
	// Convert the observed retry seconds into one fixed pause deadline.
	retryAfterUntil := updatedAt.Add(time.Duration(*snapshot.RetryAfterSeconds) * time.Second)
	return now.Before(retryAfterUntil)
}

// GrokQuotaWindowAutoPause reports whether a named positive-limit window is
// exhausted unless its observed positive Unix reset has elapsed. Missing
// counters or an elapsed positive reset returns false.
//
// ResetUnix is considered before counter arithmetic; missing, zero, or negative
// reset values leave exhaustion usable, and textual ResetAt is not consulted.
// Remaining at or below zero pauses the window, as does utilization at least
// one. Negative remaining can produce utilization above one because this
// scheduling decision returns the unbounded fraction for its selected window.
func GrokQuotaWindowAutoPause(name string, window *usageview.QuotaWindow, now time.Time) (bool, QuotaAutoPauseDecision) {
	if window == nil || window.Limit == nil || window.Remaining == nil || *window.Limit <= 0 {
		return false, QuotaAutoPauseDecision{}
	}
	if window.ResetUnix != nil && *window.ResetUnix > 0 && !now.Before(time.Unix(*window.ResetUnix, 0)) {
		return false, QuotaAutoPauseDecision{}
	}
	// Scheduling utilization is a fraction; display percentage clamping is
	// separate.
	utilization := float64(*window.Limit-*window.Remaining) / float64(*window.Limit)
	if *window.Remaining <= 0 || utilization >= 1 {
		return true, QuotaAutoPauseDecision{Window: name, Threshold: 1, Utilization: utilization}
	}
	return false, QuotaAutoPauseDecision{}
}

// grokQuotaSnapshotStaleForPause reports whether a parseable observation has
// reached the shared stale interval. Nil, missing, or invalid timestamp text
// returns false, leaving those cases to the other pause rules.
func grokQuotaSnapshotStaleForPause(snapshot *usageview.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return false
	}
	updatedAt, err := ParseUsageTime(snapshot.UpdatedAt)
	// An unknown timestamp cannot prove staleness. Retain the snapshot for
	// its retry or exhaustion checks instead of discarding its quota
	// evidence.
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= CodexAutoPauseStaleAfter
}
