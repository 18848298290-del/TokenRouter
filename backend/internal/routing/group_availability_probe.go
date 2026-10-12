package routing

import (
	"context"
	"time"
)

const (
	GroupAvailabilityProbeStatusSuccess = "success"
	GroupAvailabilityProbeStatusFailed  = "failed"
)

// GroupAvailabilityProbeDueGroup is a group snapshot claimed for an active probe.
type GroupAvailabilityProbeDueGroup struct {
	GroupID int64
	Name    string
	Config  GroupAvailabilityProbeConfig
}

// GroupAvailabilityProbeResult contains an attempt outcome; the runner persists the final outcome of each round.
type GroupAvailabilityProbeResult struct {
	GroupID      int64
	ProviderID   *int64
	ModelID      string
	Status       string
	Success      bool
	LatencyMs    int64
	ErrorMessage string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// GroupAvailabilityBucket aggregates probe results for one marketplace time bucket.
type GroupAvailabilityBucket struct {
	Date             string
	SuccessCount     int64
	TotalCount       int64
	AvailabilityRate *float64
}

// GroupAvailabilitySummary summarizes active probe results for a group.
type GroupAvailabilitySummary struct {
	WindowDays       int
	BucketMinutes    int
	SuccessCount     int64
	TotalCount       int64
	AvailabilityRate *float64
	LastStatus       string
	LastCheckedAt    *time.Time
	Days             []GroupAvailabilityBucket
}

// GroupAvailabilityProbeRepository stores probe leases, results, and availability aggregates.
type GroupAvailabilityProbeRepository interface {
	ClaimDue(ctx context.Context, now time.Time, lockUntil time.Time, lockedBy string, limit int) ([]GroupAvailabilityProbeDueGroup, error)
	SaveResultAndScheduleNext(ctx context.Context, result *GroupAvailabilityProbeResult, nextRunAt time.Time) error
	GetSummaryByGroupIDs(ctx context.Context, groupIDs []int64, days int, bucketMinutes int, timezone string, now time.Time) (map[int64]*GroupAvailabilitySummary, error)
	CleanupOldResults(ctx context.Context, before time.Time) error
}
