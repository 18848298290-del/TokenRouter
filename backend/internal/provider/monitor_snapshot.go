package provider

import (
	"time"
)

// CN usage monitoring retains the last accepted balance and limit observations
// when a later query fails. CNUsageMonitor separates its most recent attempt
// status from successful data, and snapshot identity/version checks prevent
// observations from another configured provider identity being reused.

// CNUsageMonitorError records the latest usage probe failure code and time.
type CNUsageMonitorError struct {
	Code       string    `json:"code"`        // latest usage query failure code
	ObservedAt time.Time `json:"observed_at"` // failure observation time in UTC
}

// CNUsageMonitorSnapshot holds the last successful data and the latest attempt status.
// Failed probes update LastError while retaining the successful balance and windows.
type CNUsageMonitorSnapshot struct {
	// Version, Adapter, and IdentityHash tie retained data to the monitor format
	// and query identity. CNUsageMonitor creates a fresh snapshot when version or
	// identity changes before recording the next attempt.
	Version      int    `json:"version"`       // stored usage snapshot format version
	Adapter      string `json:"adapter"`       // selected usage adapter identity
	IdentityHash string `json:"identity_hash"` // observed query identity fingerprint
	// Provider, Mode, Unit, Balance, Balances, Available, Limits, Subscription,
	// ExpiresAt, and ObservedAt are refreshed by successful queries. For a retained
	// identity, a failed query keeps these values while LastAttemptAt and LastError
	// describe the new attempt.
	Provider      string                      `json:"provider,omitempty"`     // reported upstream provider identity
	Mode          string                      `json:"mode,omitempty"`         // reported upstream usage mode
	Unit          string                      `json:"unit,omitempty"`         // reported usage amount unit
	Balance       *UpstreamUsageAmount        `json:"balance,omitempty"`      // latest primary balance observation
	Balances      []UpstreamUsageBalanceEntry `json:"balances,omitempty"`     // latest named balance observations
	Available     *bool                       `json:"available,omitempty"`    // latest reported account availability
	Limits        []UpstreamUsageLimit        `json:"limits,omitempty"`       // latest usage limit observations
	Subscription  *UpstreamUsageSubscription  `json:"subscription,omitempty"` // latest subscription plan observation
	ExpiresAt     *time.Time                  `json:"expires_at,omitempty"`   // reported account expiry time
	ObservedAt    *time.Time                  `json:"observed_at,omitempty"`  // latest successful observation time
	LastAttemptAt time.Time                   `json:"last_attempt_at"`        // latest query attempt time
	LastError     *CNUsageMonitorError        `json:"last_error,omitempty"`   // latest usage query failure
}
