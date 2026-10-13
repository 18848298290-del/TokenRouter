package usage

import (
	"context"
	"errors"
	"time"
)

// Live aggregation and administrator backfill requests can update the same
// progress row between reads. Each change kind owns a subset of fields, so a
// live watermark update keeps the current manual target and a manual request
// keeps live progress. The store supplies the latest locked row before applying
// those field-specific changes.
//
// Manual progress additionally compares the target and cursor observed by that
// worker with the current row. A later administrator request makes the older
// worker's update superseded. Read and update helpers retain the observed pair
// for the next comparison. Reads annotate the returned state, while successful
// updates replace it by value; the timestamp pointers remain shared.

// Analytics change kinds select owned fields in the singleton progress row,
// preventing unrelated live, automatic, and manual updates from replacing each other.
const (
	AnalyticsManualRequest     AnalyticsChangeKind = iota + 1 // replace the administrator target and cursor
	AnalyticsRunStarted                                       // record live-run phase and start time
	AnalyticsLiveSuccess                                      // advance live watermark and run outcome
	AnalyticsRunFailed                                        // record failed-run phase and diagnostics
	AnalyticsAutomaticProgress                                // advance automatic coverage and cursor
	AnalyticsManualProgress                                   // conditionally advance the observed manual request
)

// ErrAnalyticsRequestSuperseded tells a backfill worker that the administrator
// has replaced its observed manual target or cursor before progress publication.
var ErrAnalyticsRequestSuperseded = errors.New("manual backfill request superseded")

// AnalyticsChangeKind identifies the fields an aggregation-state update may change.
type AnalyticsChangeKind uint8

// AnalyticsStateChange carries updated fields and the expected manual-backfill
// target and cursor.
type AnalyticsStateChange struct {
	Kind AnalyticsChangeKind // owned-field update selection kind
	// State supplies the desired owned fields for Kind; ApplyAnalyticsStateChange
	// retains every other field from the latest row supplied by the store.
	State UsageAnalyticsAggregationState // desired values for owned fields
	// ExpectedManualStart and ExpectedManualCursor identify the manual request
	// read by the worker. AnalyticsManualProgress compares both with the locked
	// row before publishing or clearing its progress.
	ExpectedManualStart, ExpectedManualCursor *time.Time // observed manual request timestamp pair
}

// ApplyAnalyticsStateChange merges the fields owned by change.Kind into current
// and returns the resulting state. The repository supplies the latest row under
// its progress-row lock; this pure helper does not acquire that lock or persist
// the result.
//
// Live, automatic, and manual changes keep fields owned by the other flows.
// Manual progress compares its observed target and cursor before updating
// coverage or clearing the request, returning current and
// ErrAnalyticsRequestSuperseded if either has changed. State copies retain
// timestamp pointers from their inputs.
// @project-doc docs/operations/pre_aggregation.md#usage_state_ownership
func ApplyAnalyticsStateChange(current UsageAnalyticsAggregationState, change AnalyticsStateChange) (UsageAnalyticsAggregationState, error) {
	next := current
	desired := change.State
	setOutcome := func() {
		next.Phase = desired.Phase
		next.LastSuccessAt = desired.LastSuccessAt
		next.LastError = desired.LastError
		next.LastErrorAt = desired.LastErrorAt
		next.LastDurationMS = desired.LastDurationMS
	}
	switch change.Kind {
	case AnalyticsManualRequest:
		next.ManualBackfillStart = desired.ManualBackfillStart
		next.ManualBackfillCursor = desired.ManualBackfillCursor
		next.Phase = "backfill"
	case AnalyticsRunStarted:
		next.Phase = desired.Phase
		next.LastRunAt = desired.LastRunAt
	case AnalyticsLiveSuccess:
		next.LiveWatermark = desired.LiveWatermark
		if next.CoverageStart == nil {
			next.CoverageStart = desired.CoverageStart
			next.BackfillCursor = desired.BackfillCursor
		}
		setOutcome()
	case AnalyticsRunFailed:
		next.Phase = desired.Phase
		next.LastError = desired.LastError
		next.LastErrorAt = desired.LastErrorAt
		next.LastDurationMS = desired.LastDurationMS
	case AnalyticsAutomaticProgress:
		next.CoverageStart = desired.CoverageStart
		next.BackfillCursor = desired.BackfillCursor
		next.SourceOldestAt = desired.SourceOldestAt
		setOutcome()
	case AnalyticsManualProgress:
		// A newer request can arrive while an hour is being rebuilt. Match both
		// observed fields before an older worker advances coverage or clears that request.
		if !sameAnalyticsTime(current.ManualBackfillStart, change.ExpectedManualStart) || !sameAnalyticsTime(current.ManualBackfillCursor, change.ExpectedManualCursor) {
			return current, ErrAnalyticsRequestSuperseded
		}
		next.ManualBackfillStart = desired.ManualBackfillStart
		next.ManualBackfillCursor = desired.ManualBackfillCursor
		next.CoverageStart = desired.CoverageStart
		next.BackfillCursor = desired.BackfillCursor
		setOutcome()
	default:
		return current, errors.New("unknown analytics state change")
	}
	return next, nil
}

// sameAnalyticsTime compares optional timestamps by instant, treating two nil
// pointers as equal. A nil and nonnil pointer differ, which lets manual-request
// checks detect creation or removal of a target.
func sameAnalyticsTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// readAnalyticsState reads progress and remembers its manual target and cursor
// on a successful nonnil result. Those pointers identify the observed request
// for a later field-specific update; repository errors propagate.
func (s *DashboardAggregationService) readAnalyticsState(ctx context.Context) (*UsageAnalyticsAggregationState, error) {
	state, err := s.analyticsRepo.GetUsageAnalyticsAggregationState(ctx)
	if err == nil && state != nil {
		state.observedManualStart = state.ManualBackfillStart
		state.observedManualCursor = state.ManualBackfillCursor
	}
	return state, err
}

// updateAnalyticsState applies a kind-specific update with the state's observed
// manual request pair. Callers supply a nonnil state and repository; a
// successful nonnil result replaces the caller's state and refreshes the
// observed pair.
func (s *DashboardAggregationService) updateAnalyticsState(ctx context.Context, state *UsageAnalyticsAggregationState, kind AnalyticsChangeKind) error {
	next, err := s.analyticsRepo.ApplyUsageAnalyticsState(ctx, AnalyticsStateChange{Kind: kind, State: *state, ExpectedManualStart: state.observedManualStart, ExpectedManualCursor: state.observedManualCursor})
	if err == nil && next != nil {
		*state = *next
		state.observedManualStart = state.ManualBackfillStart
		state.observedManualCursor = state.ManualBackfillCursor
	}
	return err
}
