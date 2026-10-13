package postgres

import (
	"context"
	"time"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// API-key list rows show recent charges even for keys with no matching usage.
// Each requested key starts with a zero total, and the reader chooses covered
// analytics or one raw query over the same captured thirty-day range. The
// result uses actual_cost charged to users.

// ReadAPIKeyUsageTotals returns actual-cost sums per key over thirty calendar
// days ending at one captured system time. Missing keys retain zero, duplicate
// IDs share one map entry, and an empty input returns an empty map without
// querying.
//
// Available analytics supplies the totals; an unavailable or failed analytics
// read retries the full raw [start, now) range. Raw rows are closed before
// success, and query, scan, iteration, or closing failures return an error.
func ReadAPIKeyUsageTotals(ctx context.Context, sqlq sqlExecutor, preAggregation *preaggregation.PreAggregationSettingsService, keyIDs []int64) (map[int64]float64, error) {
	result := make(map[int64]float64, len(keyIDs))
	if len(keyIDs) == 0 {
		return result, nil
	}
	for _, id := range keyIDs {
		result[id] = 0
	}

	now := time.Now()
	// Use this clock value's calendar dates so analytics and its raw retry
	// receive identical endpoints even when the range crosses a DST change.
	start := now.AddDate(0, 0, -30)
	if preAggregation != nil {
		usageRepo := &Store{sql: sqlq, preAggregation: preAggregation}
		if stats, ok, err := usageRepo.getBatchAPIKeyUsageStatsFromAnalytics(ctx, keyIDs, start, now); err == nil && ok {
			for keyID, stat := range stats {
				if stat != nil {
					result[keyID] = stat.TotalActualCost
				}
			}
			return result, nil
		} else if err != nil {
			usageRepo.logUsageAnalyticsFallback("api_key_list_usage", err)
		}
	}

	// An unavailable analytics attempt contributes no partial totals. Query the
	// entire original range from raw rows so the retry cannot double-count covered
	// buckets.
	query := `
		SELECT api_key_id, COALESCE(SUM(actual_cost), 0)
		FROM usage_logs
		WHERE api_key_id = ANY($1)
		  AND created_at >= $2
		  AND created_at < $3
		GROUP BY api_key_id
	`
	rows, err := sqlq.QueryContext(ctx, query, pq.Array(keyIDs), now.AddDate(0, 0, -30), now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var keyID int64
		var total float64
		if err := rows.Scan(&keyID, &total); err != nil {
			// Stop consuming after a scan failure and release its result rows; return
			// the scan error that made these key totals unusable.
			_ = rows.Close()
			return nil, err
		}
		result[keyID] = total
	}
	// Finish the raw result before returning its map. A Close failure rejects
	// these totals just as an iteration failure does, while releasing the rows.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}
