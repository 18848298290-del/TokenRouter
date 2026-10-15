package usage

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// analytics_aggregation.go defines multidimensional usage-aggregation progress
// and the repository operations that maintain its buckets.
// Multidimensional usage rollups keep UTC live progress separate from reverse
// historical backfill. Their repository interface exposes hourly rebuilding,
// daily rebuilding, and field-specific progress publication, allowing the
// scheduler to save a completed cursor only after the corresponding bucket work
// succeeds.

// UsageAnalyticsAggregationState tracks the live watermark and historical
// coverage of usage rollups.
type UsageAnalyticsAggregationState struct {
	// observedManualStart and observedManualCursor capture the request pair
	// returned by readAnalyticsState or updateAnalyticsState. They remain the
	// expected pair while a worker prepares its next manual progress change.
	observedManualStart, observedManualCursor *time.Time // last-read manual target and cursor
	LiveWatermark                             time.Time  // last live scan endpoint
	// CoverageStart, BackfillCursor, and SourceOldestAt relate recorded coverage
	// to retained input. Automatic backfill scans backward from BackfillCursor
	// toward the UTC hour containing SourceOldestAt.
	// CoverageStart is nil until continuous coverage is recorded. The
	// multidimensional analytics query planner then declines a covered range.
	CoverageStart *time.Time // continuous historical coverage start
	// BackfillCursor is nil before a scan endpoint is recorded. Automatic
	// backfill starts at the current UTC hour in that case.
	BackfillCursor *time.Time // next automatic reverse-scan endpoint
	// SourceOldestAt remains nil until a retained raw timestamp is found;
	// automatic backfill skips work when the raw source is empty.
	SourceOldestAt *time.Time // oldest recorded source instant
	// ManualBackfillStart and ManualBackfillCursor form a persisted administrator
	// request. Workers retain their observed pair so a later request can supersede
	// an in-flight progress update instead of being cleared by it.
	ManualBackfillStart  *time.Time // administrator reverse-scan target instant
	ManualBackfillCursor *time.Time // administrator next scan endpoint
	Phase                string     // current aggregation status label
	LastRunAt            *time.Time // latest run start instant
	LastSuccessAt        *time.Time // latest successful run instant
	LastErrorAt          *time.Time // latest failed run instant
	LastError            string     // latest aggregation failure text
	LastDurationMS       int64      // milliseconds
}

// UsageAnalyticsAggregationRepository persists multidimensional usage rollups
// and their progress.
type UsageAnalyticsAggregationRepository interface {
	// AggregateUsageAnalyticsRange rebuilds UTC hours touched by [start, end)
	// and their derived dates. PostgreSQL treats an empty or reversed range as a
	// successful no-op; the final live hour is scanned only through end so its
	// raw tail can supply later records.
	AggregateUsageAnalyticsRange(ctx context.Context, start, end time.Time) error
	// AggregateUsageAnalyticsHourlyRange rebuilds UTC hour buckets for live or
	// reverse backfill work, scanning raw records before end. PostgreSQL accepts
	// empty or reversed ranges as no-ops. The caller rebuilds daily data after the
	// required hourly work succeeds.
	AggregateUsageAnalyticsHourlyRange(ctx context.Context, start, end time.Time) error
	// RebuildUsageAnalyticsDailyRange rebuilds complete UTC dates touched by
	// [start, end) from hourly rollups. Callers finish the required hourly work
	// first; PostgreSQL returns successfully for an empty or reversed range.
	RebuildUsageAnalyticsDailyRange(ctx context.Context, start, end time.Time) error
	// RecomputeUsageAnalyticsRange repairs complete UTC buckets touched by a
	// positive-width inclusive cleanup range. PostgreSQL returns successfully
	// when end does not follow start, so equal endpoints request no repair.
	// Rescanning through the final bucket's end keeps retained records outside
	// the deletion represented.
	RecomputeUsageAnalyticsRange(ctx context.Context, start, end time.Time) error
	// GetUsageAnalyticsAggregationState reads the singleton live and historical
	// progress row.
	GetUsageAnalyticsAggregationState(ctx context.Context) (*UsageAnalyticsAggregationState, error)
	// ApplyUsageAnalyticsState merges a field-specific change into the latest
	// progress row and returns that row. Manual progress returns
	// ErrAnalyticsRequestSuperseded when its expected request pair differs from
	// the stored pair.
	ApplyUsageAnalyticsState(ctx context.Context, change AnalyticsStateChange) (*UsageAnalyticsAggregationState, error)
	// GetOldestUsageLogTime returns the oldest retained raw usage timestamp, or
	// nil for an empty source. Automatic backfill uses that instant to stop
	// reverse scanning after its oldest hour.
	GetOldestUsageLogTime(ctx context.Context) (*time.Time, error)
	// CleanupUsageAnalytics removes hourly and daily rollups older than their
	// supplied cutoffs. These deletions reduce retained report history; callers
	// coordinate their cutoff values with raw-record retention and query coverage.
	CleanupUsageAnalytics(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error
}

// PreAggregationRuntimeStatus is the shared settings-page view of aggregation
// runtime progress.
type PreAggregationRuntimeStatus = preaggregation.PreAggregationRuntimeStatus
