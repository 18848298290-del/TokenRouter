package provider

import (
	"context"
)

// TempUnschedState records a temporary scheduling pause and its trigger.
type TempUnschedState struct {
	UntilUnix            int64  `json:"until_unix"`                       // Pause expiry, in Unix seconds.
	TriggeredAtUnix      int64  `json:"triggered_at_unix"`                // Trigger time, in Unix seconds.
	StatusCode           int    `json:"status_code"`                      // Triggering HTTP status; zero for a stream timeout.
	MatchedKeyword       string `json:"matched_keyword"`                  // Matched keyword or system reason.
	RuleIndex            int    `json:"rule_index"`                       // Provider rule index; -1 for a system rule.
	ErrorMessage         string `json:"error_message"`                    // Diagnostic message.
	TriggerCount         int64  `json:"trigger_count,omitempty"`          // Accumulated count at the trigger.
	TriggerThreshold     int    `json:"trigger_threshold,omitempty"`      // Count required to trigger the pause.
	TriggerWindowMinutes int    `json:"trigger_window_minutes,omitempty"` // Counting window in minutes.
}

// TempUnschedCache stores temporary scheduling pause state by provider ID.
type TempUnschedCache interface {
	SetTempUnsched(ctx context.Context, providerID int64, state *TempUnschedState) error
	GetTempUnsched(ctx context.Context, providerID int64) (*TempUnschedState, error)
	DeleteTempUnsched(ctx context.Context, providerID int64) error
}

// OpenAIAPIKeyHealthCache aggregates pool API-key failures across gateway instances.
// Its rolling-window counter reports when the failure threshold is reached.
type OpenAIAPIKeyHealthCache interface {
	RecordOpenAIAPIKeyHealthFailure(ctx context.Context, providerID int64, windowMinutes, threshold int) (count int64, tripped bool, err error)
}
