package provider

import (
	"context"
)

// Temporary health pauses carry a deadline and their triggering evidence for
// cache and administrator display. Rule-generated pauses retain their rule
// index, while system actions use a sentinel; shared API Key failure accounting
// separately selects when a rolling count requests a breaker pause.

// TempUnschedState holds deadline and diagnostic evidence for a provider-wide
// temporary scheduling pause.
type TempUnschedState struct {
	UntilUnix       int64  `json:"until_unix"`        // Pause expiry, in Unix seconds.
	TriggeredAtUnix int64  `json:"triggered_at_unix"` // Trigger time, in Unix seconds.
	StatusCode      int    `json:"status_code"`       // Triggering HTTP status; zero for a stream timeout.
	MatchedKeyword  string `json:"matched_keyword"`   // Matched keyword or system reason.
	RuleIndex       int    `json:"rule_index"`        // Provider rule index; -1 for a system rule.
	ErrorMessage    string `json:"error_message"`     // stored upstream diagnostic
	// TriggerCount, TriggerThreshold and TriggerWindowMinutes describe the
	// counted breaker event behind this pause. The API Key breaker populates all
	// three; ordinary rule and stream-timeout builders leave them zero.
	TriggerCount         int64 `json:"trigger_count,omitempty"`          // failure count at trigger
	TriggerThreshold     int   `json:"trigger_threshold,omitempty"`      // failure count needed to trip
	TriggerWindowMinutes int   `json:"trigger_window_minutes,omitempty"` // Counting window in minutes.
}

// TempUnschedCache supplies deadline-bearing provider pause state separately
// from persistent health writes.
type TempUnschedCache interface {
	// SetTempUnsched publishes a pause state for the provider. Supply a nonnil
	// state and keep it stable through the call; the production Redis adapter
	// ignores expired observations and retains a longer cached deadline with its
	// metadata.
	SetTempUnsched(ctx context.Context, providerID int64, state *TempUnschedState) error
	// GetTempUnsched reads a provider pause or returns nil for an absent cache
	// entry. Cache and decoding errors remain available to recovery callers, which
	// decide whether a persistent fallback can be attempted.
	GetTempUnsched(ctx context.Context, providerID int64) (*TempUnschedState, error)
	// DeleteTempUnsched removes the provider's cached pause after recovery chooses
	// cleanup. An absent pause succeeds; backend deletion errors remain separate
	// from any already completed persistent state clear.
	DeleteTempUnsched(ctx context.Context, providerID int64) error
}

// OpenAIAPIKeyHealthCache supplies rolling failure counts used by the OpenAI
// pool API Key breaker.
type OpenAIAPIKeyHealthCache interface {
	// RecordOpenAIAPIKeyHealthFailure records a provider failure and returns its
	// current count and threshold result. Window minutes and threshold define the
	// shared rolling decision, while errors leave the health core to skip breaker
	// publication.
	//
	// The production Redis adapter uses Redis time, removes expired events and
	// allocates distinct members for simultaneous failures. Nonpositive window and
	// threshold inputs select one minute and one failure. Reaching threshold
	// clears that counted window before reporting a trip. The caller subsequently
	// decides whether pause storage succeeds, so a trip result itself confirms
	// accounting rather than a saved health change.
	RecordOpenAIAPIKeyHealthFailure(ctx context.Context, providerID int64, windowMinutes, threshold int) (count int64, tripped bool, err error)
}
