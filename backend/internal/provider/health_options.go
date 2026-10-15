package provider

import (
	"context"
	"time"
)

// health_options.go defines stream-timeout and overload policies and the
// shared timeout-counter interface.
// Stream timeout policy decides whether observed failures are counted and how a
// reached threshold affects provider health. The timeout detector supplies the
// failure itself; these values govern temporary pauses, account errors or a
// skipped action, while 529 overload uses a separate enabled/duration policy.

// Stream timeout action values distinguish a temporary pause, account error or
// skipped health command after policy evaluation selects an action.
const (
	StreamTimeoutActionTempUnsched = "temp_unsched" // Temporarily pause scheduling.
	StreamTimeoutActionError       = "error"        // Mark the provider as errored.
	StreamTimeoutActionNone        = "none"         // Leave provider state unchanged.
)

// StreamTimeoutSettings supplies counting and resulting health-action policy
// for detected stream timeouts.
type StreamTimeoutSettings struct {
	// Enabled and Action decide whether HandleStreamTimeout records a failure at
	// all. Disabled policy or the none action returns before counter access; other
	// actions can proceed to threshold evaluation.
	Enabled bool   `json:"enabled"` // timeout handling enabled
	Action  string `json:"action"`  // selected health action
	// TempUnschedMinutes supplies the pause duration when Action selects temporary
	// suspension; error handling uses an account-error command instead.
	TempUnschedMinutes int `json:"temp_unsched_minutes"`
	// ThresholdCount and ThresholdWindowMinutes describe the shared counter
	// decision. The production Redis cache fixes expiry at the first increment;
	// policy changes during that lifetime do not extend its end.
	ThresholdCount         int `json:"threshold_count"`          // timeout trip count
	ThresholdWindowMinutes int `json:"threshold_window_minutes"` // minutes
}

// TimeoutCounterCache supplies expiring provider timeout counts and independent
// history cleanup operations.
type TimeoutCounterCache interface {
	// IncrementTimeoutCount records a timeout and returns the count used for
	// health thresholds. The production Redis adapter gives the first increment a
	// fixed window, with a one-minute minimum; later increments retain its expiry.
	IncrementTimeoutCount(ctx context.Context, providerID int64, windowMinutes int) (int64, error)
	// GetTimeoutCount reads the selected provider's shared timeout count,
	// returning zero for an absent history. Backend errors remain separate so
	// consumers can distinguish no observations from an unavailable counter.
	GetTimeoutCount(ctx context.Context, providerID int64) (int64, error)
	// ResetTimeoutCount removes the selected provider's shared history after
	// health handling or recovery. Missing history succeeds, while deletion errors
	// remain available for callers to log incomplete cleanup.
	ResetTimeoutCount(ctx context.Context, providerID int64) error
	// GetTimeoutCountTTL returns the counter's remaining backend lifetime for
	// status presentation. Nonpositive values indicate a backend sentinel or
	// absent live countdown; storage errors are returned separately from that
	// duration.
	GetTimeoutCountTTL(ctx context.Context, providerID int64) (time.Duration, error)
}

// OverloadCooldownSettings supplies the enabled switch and duration for
// provider-wide HTTP 529 handling.
type OverloadCooldownSettings struct {
	// Enabled and CooldownMinutes control ApplyOverload together. Disabled policy
	// returns before a health write; enabled policy uses the duration or resolves
	// a built-in value when it is nonpositive.
	Enabled         bool `json:"enabled"`          // overload handling enabled
	CooldownMinutes int  `json:"cooldown_minutes"` // minutes
}
