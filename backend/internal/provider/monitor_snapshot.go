package provider

import (
	"time"
)

// CNUsageMonitorError records the latest probe failure code and time.
type CNUsageMonitorError struct {
	Code       string    `json:"code"`
	ObservedAt time.Time `json:"observed_at"`
}

// CNUsageMonitorSnapshot holds the last successful data and the latest attempt status.
// Failed probes update LastError while retaining the successful balance and windows.
type CNUsageMonitorSnapshot struct {
	Version       int                         `json:"version"`
	Adapter       string                      `json:"adapter"`
	IdentityHash  string                      `json:"identity_hash"`
	Provider      string                      `json:"provider,omitempty"`
	Mode          string                      `json:"mode,omitempty"`
	Unit          string                      `json:"unit,omitempty"`
	Balance       *UpstreamUsageAmount        `json:"balance,omitempty"`
	Balances      []UpstreamUsageBalanceEntry `json:"balances,omitempty"`
	Available     *bool                       `json:"available,omitempty"`
	Limits        []UpstreamUsageLimit        `json:"limits,omitempty"`
	Subscription  *UpstreamUsageSubscription  `json:"subscription,omitempty"`
	ExpiresAt     *time.Time                  `json:"expires_at,omitempty"`
	ObservedAt    *time.Time                  `json:"observed_at,omitempty"`
	LastAttemptAt time.Time                   `json:"last_attempt_at"`
	LastError     *CNUsageMonitorError        `json:"last_error,omitempty"`
}
