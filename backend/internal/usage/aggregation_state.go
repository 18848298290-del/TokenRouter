package usage

import (
	"context"
	"errors"
	"time"
)

const (
	AnalyticsManualRequest AnalyticsChangeKind = iota + 1
	AnalyticsRunStarted
	AnalyticsLiveSuccess
	AnalyticsRunFailed
	AnalyticsAutomaticProgress
	AnalyticsManualProgress
)

var ErrAnalyticsRequestSuperseded = errors.New("manual backfill request superseded")

// AnalyticsChangeKind identifies the fields an aggregation-state update may change.
type AnalyticsChangeKind uint8

// AnalyticsStateChange carries updated fields and the expected manual-backfill target and cursor.
type AnalyticsStateChange struct {
	Kind                                      AnalyticsChangeKind
	State                                     UsageAnalyticsAggregationState
	ExpectedManualStart, ExpectedManualCursor *time.Time
}

// ApplyAnalyticsStateChange applies the change's fields to the latest row locked by the store.
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

func sameAnalyticsTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (s *DashboardAggregationService) readAnalyticsState(ctx context.Context) (*UsageAnalyticsAggregationState, error) {
	state, err := s.analyticsRepo.GetUsageAnalyticsAggregationState(ctx)
	if err == nil && state != nil {
		state.observedManualStart = state.ManualBackfillStart
		state.observedManualCursor = state.ManualBackfillCursor
	}
	return state, err
}

func (s *DashboardAggregationService) updateAnalyticsState(ctx context.Context, state *UsageAnalyticsAggregationState, kind AnalyticsChangeKind) error {
	next, err := s.analyticsRepo.ApplyUsageAnalyticsState(ctx, AnalyticsStateChange{Kind: kind, State: *state, ExpectedManualStart: state.observedManualStart, ExpectedManualCursor: state.observedManualCursor})
	if err == nil && next != nil {
		*state = *next
		state.observedManualStart = state.ManualBackfillStart
		state.observedManualCursor = state.ManualBackfillCursor
	}
	return err
}
