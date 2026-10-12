package provider

import (
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// EvaluateGrokQuotaAutoPause reads the quota snapshot, then samples the clock to decide whether scheduling
// should pause.
func EvaluateGrokQuotaAutoPause(provider *Record, clock func() time.Time) (bool, QuotaAutoPauseDecision) {
	if provider == nil || !provider.IsGrok() || provider.Type != capability.ProviderTypeOAuth {
		return false, QuotaAutoPauseDecision{}
	}
	snapshot, err := GrokQuotaSnapshotFromExtra(provider.Extra)
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

func grokQuotaRetryAfterActive(snapshot *usageview.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || snapshot.RetryAfterSeconds == nil || *snapshot.RetryAfterSeconds <= 0 {
		return false
	}
	if strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return true
	}
	updatedAt, err := ParseUsageTime(snapshot.UpdatedAt)
	if err != nil {
		return true
	}
	retryAfterUntil := updatedAt.Add(time.Duration(*snapshot.RetryAfterSeconds) * time.Second)
	return now.Before(retryAfterUntil)
}

// GrokQuotaWindowAutoPause reports whether an unexpired observed window has exhausted its quota.
func GrokQuotaWindowAutoPause(name string, window *usageview.QuotaWindow, now time.Time) (bool, QuotaAutoPauseDecision) {
	if window == nil || window.Limit == nil || window.Remaining == nil || *window.Limit <= 0 {
		return false, QuotaAutoPauseDecision{}
	}
	if window.ResetUnix != nil && *window.ResetUnix > 0 && !now.Before(time.Unix(*window.ResetUnix, 0)) {
		return false, QuotaAutoPauseDecision{}
	}
	utilization := float64(*window.Limit-*window.Remaining) / float64(*window.Limit)
	if *window.Remaining <= 0 || utilization >= 1 {
		return true, QuotaAutoPauseDecision{Window: name, Threshold: 1, Utilization: utilization}
	}
	return false, QuotaAutoPauseDecision{}
}

func grokQuotaSnapshotStaleForPause(snapshot *usageview.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return false
	}
	updatedAt, err := ParseUsageTime(snapshot.UpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= CodexAutoPauseStaleAfter
}
