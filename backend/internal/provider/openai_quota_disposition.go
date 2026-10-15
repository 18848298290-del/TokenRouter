package provider

import (
	"time"

	openaiprotocol "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// openai_quota_disposition.go classifies exhausted Codex windows while
// preserving exhaustion without reset timing.
// Codex quota exhaustion can be known even when no reset time is supplied. The
// normalized seven-day window has precedence over five hours; the resulting
// disposition lets retry callers distinguish known exhaustion from a transient
// response with no quota evidence.
//
// Utilization at or above one hundred percent selects that window before
// inspecting its optional reset. Relative reset seconds are added directly to
// the injected clock, so callers supply representable intervals and decide
// whether the resulting deadline is useful. A nil reset retains the exhaustion
// category and blocks transient-only recovery paths. Generic reset
// observations have their own category for callers that inspect headers or
// bodies after this helper.

// OpenAI429 dispositions separate retryable absence of quota evidence from
// known exhaustion and generic reset signals used by response observers.
const (
	OpenAI429Transient  OpenAI429Disposition = iota // no exhausted-window or reset evidence
	OpenAI429Quota5h                                // exhausted normalized five-hour window
	OpenAI429Quota7d                                // exhausted normalized seven-day window
	OpenAI429QuotaReset                             // reset supplied without named exhaustion
)

// OpenAI429Disposition identifies transient, exhausted-window, or generic
// reset evidence.
type OpenAI429Disposition uint8

// OpenAIExhaustedWindow returns normalized quota exhaustion with an optional
// reset deadline. Nil or unusable snapshots return transient; a clock callback
// is required when an exhausted window supplies reset seconds.
//
// Seven-day utilization wins before five-hour utilization, including when its
// reset is absent. A nil deadline therefore can accompany a quota category and
// must not be interpreted as transient. Supplied reset seconds are converted
// directly to a duration from the clock, retaining zero or negative intervals
// for the caller to evaluate.
func OpenAIExhaustedWindow(snapshot *openaiprotocol.OpenAICodexUsageSnapshot, clock func() time.Time) (OpenAI429Disposition, *time.Time) {
	if snapshot == nil {
		return OpenAI429Transient, nil
	}
	normalized := snapshot.Normalize()
	if normalized == nil {
		return OpenAI429Transient, nil
	}
	// Check the weekly observation first because it can remain exhausted after
	// a short window resets. Missing weekly reset seconds still retain that quota
	// category.
	if normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100 {
		if normalized.Reset7dSeconds != nil {
			// Relative reset seconds anchor the reported window to this clock sample.
			reset := clock().Add(time.Duration(*normalized.Reset7dSeconds) * time.Second)
			return OpenAI429Quota7d, &reset
		}
		return OpenAI429Quota7d, nil
	}
	// The weekly branch did not identify exhaustion, so the short window can
	// determine quota state independently and keep its own optional relative
	// reset.
	if normalized.Used5hPercent != nil && *normalized.Used5hPercent >= 100 {
		if normalized.Reset5hSeconds != nil {
			// Relative reset seconds anchor the short window to this clock sample.
			reset := clock().Add(time.Duration(*normalized.Reset5hSeconds) * time.Second)
			return OpenAI429Quota5h, &reset
		}
		return OpenAI429Quota5h, nil
	}
	return OpenAI429Transient, nil
}
