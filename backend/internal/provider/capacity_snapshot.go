package provider

import (
	"time"
)

// capacity_snapshot.go builds scalar provider capacity snapshots from runtime
// configuration and observed quota-pause decisions.
// Group capacity queries read concurrency and runtime limits together with quota
// observations at one supplied time. ProjectObservedCapacity delegates pause
// evaluation to the platform quota rules, then returns scalar capacity values
// that remain separate from the row's mutable Extra data.

// GroupProviderCapacityRow contains stored fields used when observing one group's capacity.
type GroupProviderCapacityRow struct {
	GroupID     int64  // owning routing group identifier
	ProviderID  int64  // observed provider record identifier
	Platform    string // configured upstream platform identity
	Concurrency int    // configured request concurrency limit
	// Extra supplies runtime limits and quota observations to ProjectObservedCapacity.
	// The row borrows this map; callers keep it stable while the observation is calculated.
	Extra map[string]any
	// SessionWindowStart and SessionWindowEnd retain optional stored window times.
	// They are passed to RuntimeConfig during observation, while the resulting
	// capacity snapshot contains scalar limits and no window pointers.
	SessionWindowStart  *time.Time // stored session window start
	SessionWindowEnd    *time.Time // stored session window end
	SessionWindowStatus string     // stored session window status
}

// CapacitySnapshot contains capacity and scheduling data without credentials or raw Extra values.
type CapacitySnapshot struct {
	ID                        int64 // observed provider record identifier
	Concurrency               int   // configured request concurrency limit
	MaxSessions               int   // configured active session limit
	SessionIdleTimeoutMinutes int   // minutes
	BaseRPM                   int   // requests/minute
	QuotaAutoPaused           bool  // observed quota pause decision
}

// LoadObservation identifies a provider and its effective load denominator for Ops.
type LoadObservation struct {
	ID             int64 // observed provider record identifier
	MaxConcurrency int   // effective load factor denominator
}

// ProjectCapacity returns scalar runtime limits and the supplied quota pause
// result. Missing or nonpositive idle timeout uses RuntimeConfig's five-minute
// default, while missing RPM produces zero through the runtime accessors.
func ProjectCapacity(id int64, config RuntimeConfig, quotaAutoPaused bool) CapacitySnapshot {
	return CapacitySnapshot{ID: id, Concurrency: config.Concurrency, MaxSessions: config.GetMaxSessions(), SessionIdleTimeoutMinutes: config.GetSessionIdleTimeoutMinutes(), BaseRPM: config.GetBaseRPM(), QuotaAutoPaused: quotaAutoPaused}
}

// ProjectObservedCapacity evaluates quota state at now and combines that
// result with runtime capacity limits. The returned scalar values can be changed
// independently of Extra; detailed pause reasons are discarded for this capacity view.
func ProjectObservedCapacity(row GroupProviderCapacityRow, settings QuotaAutoPauseSettings, now time.Time) CapacitySnapshot {
	paused, _ := EvaluateQuotaAutoPause(row.Platform, row.Extra, settings, now)
	return ProjectCapacity(row.ProviderID, RuntimeConfig{Extra: row.Extra, Concurrency: row.Concurrency, SessionWindowStart: row.SessionWindowStart, SessionWindowEnd: row.SessionWindowEnd}, paused)
}
