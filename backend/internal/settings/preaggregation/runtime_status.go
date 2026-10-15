package preaggregation

import "time"

// runtime_status.go defines the aggregation task observations returned by the
// settings API.
// Usage and Ops expose different aggregation observations through the settings
// API. Usage supplies coverage and watermark fields from persisted analytics state,
// while Ops supplies hourly-job heartbeat results; absent observations remain nil
// or zero in this shared response shape.

// PreAggregationRuntimeStatus carries task observations rendered in administrator
// preaggregation settings responses.
type PreAggregationRuntimeStatus struct {
	Phase string `json:"phase"` // producer-selected aggregation execution phase
	// LiveWatermark, CoverageStart, and SourceOldestAt are usage observations.
	// Nil means the producer supplied no timestamp, and Ops leaves them unset.
	// Usage copies LiveWatermark into a UTC value; CoverageStart and SourceOldestAt
	// retain the state pointers, which consumers treat as immutable.
	LiveWatermark  *time.Time `json:"live_watermark,omitempty"`   // usage completed live aggregation time
	CoverageStart  *time.Time `json:"coverage_start,omitempty"`   // usage continuous aggregate coverage start
	SourceOldestAt *time.Time `json:"source_oldest_at,omitempty"` // usage oldest retained source timestamp
	// LagSeconds is the usage producer's nonnegative whole-second watermark lag.
	// It stays zero when no valid live watermark was supplied, including Ops responses.
	LagSeconds int64 `json:"lag_seconds"` // seconds
	// LastRunAt, LastSuccessAt, and LastErrorAt retain pointers from the usage
	// state or Ops heartbeat returned by their producer. Consumers treat those
	// times as immutable; the shared response does not clone them.
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`     // latest observed job start timestamp
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"` // latest observed successful job timestamp
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`   // latest observed failed job timestamp
	LastError      string     `json:"last_error,omitempty"`      // latest producer diagnostic error text
	LastDurationMS int64      `json:"last_duration_ms"`          // milliseconds
}
