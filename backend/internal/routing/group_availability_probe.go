package routing

import (
	"context"
	"time"
)

// group_availability_probe.go defines probe outcomes, availability summaries,
// and their persistence interface.
// Availability history stores one final outcome per probe round, independently
// of transient failures within retries. Group summaries combine counts in a
// bounded window with the latest recorded status, and a bucket without
// observations keeps an unknown ratio rather than reporting failure.

// Probe outcome labels distinguish successful execution from failures in stored
// history and latest-state responses returned to marketplace consumers.
const (
	GroupAvailabilityProbeStatusSuccess = "success" // completed successful probe
	GroupAvailabilityProbeStatusFailed  = "failed"  // completed failed probe
)

// GroupAvailabilityProbeDueGroup contains the saved configuration claimed for
// one probe round.
type GroupAvailabilityProbeDueGroup struct {
	GroupID int64                        // claimed group row ID
	Name    string                       // claimed administrative group name
	Config  GroupAvailabilityProbeConfig // claimed availability probe settings
}

// GroupAvailabilityProbeResult contains one attempt outcome selected for a
// round's final sample.
type GroupAvailabilityProbeResult struct {
	GroupID int64 // observed group row ID
	// ProviderID is absent when provider selection fails or a synthetic
	// configuration failure is recorded. A selected test attempt retains the
	// provider ID even when execution fails.
	ProviderID *int64 // selected provider row ID
	// ModelID records the configured request name so probe history remains tied to
	// the administrator's selection while testing executes the separately mapped
	// provider model.
	ModelID string // configured request model ID
	// Status and Success describe the selected attempt outcome. Success stops
	// retrying and supplies summary counts, while Status becomes the stored
	// latest-state label for the group.
	Status       string // persisted probe status string
	Success      bool   // attempt success decision flag
	LatencyMs    int64  // attempt latency in milliseconds
	ErrorMessage string // attempt failure diagnostic text
	// StartedAt and FinishedAt belong to the final selected attempt rather than
	// the whole retry round. History window counts use StartedAt, and latest
	// checked time uses FinishedAt.
	StartedAt  time.Time // recorded attempt start time
	FinishedAt time.Time // recorded attempt finish time
}

// GroupAvailabilityBucket aggregates recorded probe outcomes within one
// marketplace time bucket.
type GroupAvailabilityBucket struct {
	Date string // formatted time-bucket start label
	// SuccessCount and TotalCount count saved round observations within the
	// bucket. AvailabilityRate divides those counts and is nil when the bucket has
	// no observations.
	SuccessCount     int64    // successful observations in bucket
	TotalCount       int64    // all observations in bucket
	AvailabilityRate *float64 // success fraction in bucket
}

// GroupAvailabilitySummary combines window observations with the latest
// recorded probe status.
type GroupAvailabilitySummary struct {
	WindowDays    int // nominal observation-window day count
	BucketMinutes int // observation bucket width in minutes
	// SuccessCount and TotalCount count saved rounds within the observation
	// window. AvailabilityRate is their success fraction and remains nil when that
	// window contains no observations.
	SuccessCount     int64    // successful observations in window
	TotalCount       int64    // all observations in window
	AvailabilityRate *float64 // window success fraction
	// LastStatus and LastCheckedAt come from the latest history row by start time
	// and ID. That row can precede the observation window, and a missing row
	// remains unknown.
	LastStatus    string                    // latest observed status string
	LastCheckedAt *time.Time                // latest observation finish time
	Days          []GroupAvailabilityBucket // ordered observation time buckets
}

// GroupAvailabilityProbeRepository manages probe claims, atomic result
// scheduling, and observation queries.
type GroupAvailabilityProbeRepository interface {
	// ClaimDue leases due groups until lockUntil for the supplied instance
	// identity. PostgreSQL selects ten when limit is nonpositive and otherwise
	// caps the batch at that positive limit; callers supply their worker bound and
	// launch claimed groups promptly.
	ClaimDue(ctx context.Context, now time.Time, lockUntil time.Time, lockedBy string, limit int) ([]GroupAvailabilityProbeDueGroup, error)
	// SaveResultAndScheduleNext persists one final observation and advances the
	// group's next-run time. A successful PostgreSQL commit publishes history and
	// clears the claim lease together.
	//
	// The history insertion and schedule update share a transaction, so a later
	// storage failure rolls the observation back rather than leaving a sample with
	// an unrelated schedule. Callers supply the round context and retain failed
	// claims for lease recovery if saving cannot complete. PostgreSQL addresses the
	// schedule by GroupID alone, so callers finish within the claim lease; a late
	// save can clear a newer owner's lease.
	SaveResultAndScheduleNext(ctx context.Context, result *GroupAvailabilityProbeResult, nextRunAt time.Time) error
	// GetSummaryByGroupIDs returns window counts, time buckets, and the latest
	// status for selected groups. Ratios use success divided by total and remain
	// nil when unobserved; latest status is queried separately and may precede the
	// window.
	GetSummaryByGroupIDs(ctx context.Context, groupIDs []int64, days int, bucketMinutes int, timezone string, now time.Time) (map[int64]*GroupAvailabilitySummary, error)
	// CleanupOldResults deletes stored probe observations older than the supplied
	// cutoff. The runner schedules this as retention maintenance with its own
	// timeout, allowing a failure to leave probes and later cleanup attempts
	// available.
	CleanupOldResults(ctx context.Context, before time.Time) error
}
