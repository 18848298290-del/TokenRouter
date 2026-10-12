package provider

import (
	"context"
	"time"
)

// Stream timeout actions select how provider scheduling reacts after the threshold is reached.
const (
	StreamTimeoutActionTempUnsched = "temp_unsched" // Temporarily pause scheduling.
	StreamTimeoutActionError       = "error"        // Mark the provider as errored.
	StreamTimeoutActionNone        = "none"         // Leave provider state unchanged.
)

// StreamTimeoutSettings controls the response to timeouts; gateway settings determine when a stream times out.
type StreamTimeoutSettings struct {
	// Enabled turns on stream-timeout health handling.
	Enabled bool `json:"enabled"`
	// Action is temp_unsched, error, or none.
	Action string `json:"action"`
	// TempUnschedMinutes is the scheduling pause duration in minutes.
	TempUnschedMinutes int `json:"temp_unsched_minutes"`
	// ThresholdCount is the number of timeouts required to trigger the action.
	ThresholdCount int `json:"threshold_count"`
	// ThresholdWindowMinutes is the counting window in minutes.
	ThresholdWindowMinutes int `json:"threshold_window_minutes"`
}

// TimeoutCounterCache stores provider timeout counts in expiring windows.
type TimeoutCounterCache interface {
	// IncrementTimeoutCount increments the provider count and returns its new value.
	// windowMinutes sets the counter's expiration window in minutes.
	IncrementTimeoutCount(ctx context.Context, providerID int64, windowMinutes int) (int64, error)
	// GetTimeoutCount returns the current count for a provider.
	GetTimeoutCount(ctx context.Context, providerID int64) (int64, error)
	// ResetTimeoutCount clears the provider's count.
	ResetTimeoutCount(ctx context.Context, providerID int64) error
	// GetTimeoutCountTTL returns the counter's remaining lifetime.
	GetTimeoutCountTTL(ctx context.Context, providerID int64) (time.Duration, error)
}

// OverloadCooldownSettings configures cooldowns for HTTP 529 overload responses.
type OverloadCooldownSettings struct {
	// Enabled pauses provider scheduling after a 529 response.
	Enabled bool `json:"enabled"`
	// CooldownMinutes is the pause duration in minutes.
	CooldownMinutes int `json:"cooldown_minutes"`
}
