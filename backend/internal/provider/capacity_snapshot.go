package provider

import (
	"time"
)

// GroupProviderCapacityRow contains provider fields used to aggregate group capacity.
type GroupProviderCapacityRow struct {
	GroupID             int64
	ProviderID          int64
	Platform            string
	Concurrency         int
	Extra               map[string]any
	SessionWindowStart  *time.Time
	SessionWindowEnd    *time.Time
	SessionWindowStatus string
}

// CapacitySnapshot contains capacity and scheduling data without credentials or raw Extra values.
type CapacitySnapshot struct {
	ID                        int64
	Concurrency               int
	MaxSessions               int
	SessionIdleTimeoutMinutes int
	BaseRPM                   int
	QuotaAutoPaused           bool
}

type LoadObservation struct {
	ID             int64
	MaxConcurrency int
}

func ProjectCapacity(id int64, config RuntimeConfig, quotaAutoPaused bool) CapacitySnapshot {
	return CapacitySnapshot{ID: id, Concurrency: config.Concurrency, MaxSessions: config.GetMaxSessions(), SessionIdleTimeoutMinutes: config.GetSessionIdleTimeoutMinutes(), BaseRPM: config.GetBaseRPM(), QuotaAutoPaused: quotaAutoPaused}
}

// ProjectObservedCapacity combines runtime configuration with quota thresholds at the supplied observation time.
func ProjectObservedCapacity(row GroupProviderCapacityRow, settings QuotaAutoPauseSettings, now time.Time) CapacitySnapshot {
	paused, _ := EvaluateQuotaAutoPause(row.Platform, row.Extra, settings, now)
	return ProjectCapacity(row.ProviderID, RuntimeConfig{Extra: row.Extra, Concurrency: row.Concurrency, SessionWindowStart: row.SessionWindowStart, SessionWindowEnd: row.SessionWindowEnd}, paused)
}
