package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// ops_repo_window_stats.go reads raw usage, token, and final-client error
// counts for bounded request windows.
// Window statistics combine usage-row counts, consumed tokens, and final-client
// errors for one requested range of at most twenty-four hours. Usage and error
// reads execute separately, so the returned summary shares range endpoints
// while its counts can observe different database states.

// GetWindowStats returns raw usage, token, and final-client error counts with
// UTC endpoints for a required store, database, and filter. Both endpoints must
// be nonzero, ordered, and no more than twenty-four elapsed hours apart; equal
// endpoints are accepted.
//
// Usage and error reads execute sequentially without a common transaction, and
// QueryMode is ignored. Either query failure prevents a summary result.
func (r *Store) GetWindowStats(ctx context.Context, filter *ops.OpsDashboardFilter) (*ops.OpsWindowStats, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()
	if start.After(end) {
		return nil, fmt.Errorf("start_time must be <= end_time")
	}
	// Limit this raw window-statistics query to one elapsed day.
	if end.Sub(start) > 24*time.Hour {
		return nil, fmt.Errorf("window too large")
	}

	// Read usage totals before final-client error counts using the same UTC
	// endpoints; an error in either read prevents summary publication.
	successCount, tokenConsumed, err := r.queryUsageCounts(ctx, filter, start, end)
	if err != nil {
		return nil, err
	}

	errorTotal, _, _, _, _, _, err := r.queryErrorCounts(ctx, filter, start, end)
	if err != nil {
		return nil, err
	}

	return &ops.OpsWindowStats{
		StartTime: start,
		EndTime:   end,

		SuccessCount:    successCount,
		ErrorCountTotal: errorTotal,
		TokenConsumed:   tokenConsumed,
	}, nil
}
