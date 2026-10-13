package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Latency histograms map five ordered millisecond cutoffs to six displayed
// buckets. SQL uses strictly-less-than comparisons, so a duration equal to a
// cutoff enters the following bucket; nonnull negative durations also enter the
// first bucket even though its label starts at zero. Missing groups in the SQL
// result are restored as zero-count display buckets.
//
// The repository validates or defaults cutoffs before building parameter
// placeholders and preserves their order in returned metadata. Counts come from
// filtered usage rows with nonnull duration, using raw tables regardless of
// QueryMode. Response boundary storage is copied by normalization, while
// GroupID retains the filter's pointer. Invalid bucket indexes or cursor
// failures reject the result before any response is returned.

// GetLatencyHistogram returns six raw usage-duration buckets for a required
// repository, filter, and nonzero endpoints. It normalizes five positive
// ordered cutoffs, defaults an empty list, and converts time endpoints to UTC
// without validating their relative order here.
//
// Cutoff equality belongs to the following bucket, and negative nonnull
// durations count in the first bucket. Missing SQL buckets become zero, while
// invalid indexes and query/scan/cursor errors reject the complete response.
// Returned cutoffs have independent slice storage, GroupID remains shared with
// the filter, and QueryMode is not consulted by this implementation.
func (r *Store) GetLatencyHistogram(ctx context.Context, filter *ops.OpsDashboardFilter, bucketBoundariesMS []int64) (*ops.OpsLatencyHistogramResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}
	boundaries, err := ops.NormalizeOpsLatencyBucketBoundariesMS(bucketBoundariesMS)
	if err != nil {
		return nil, err
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()

	join, where, args, next := buildUsageWhere(filter, start, end, 1)
	// Reserve one placeholder per normalized cutoff after the filter's arguments
	// so SQL bucket indexes and returned labels use the same ordered boundaries.
	bucketExpr := latencyHistogramBucketIndexCaseExpr("ul.duration_ms", next, len(boundaries))
	for _, boundary := range boundaries {
		args = append(args, boundary)
	}

	q := `
SELECT
  ` + bucketExpr + ` AS bucket_index,
  COALESCE(COUNT(*), 0) AS count
FROM usage_logs ul
` + join + `
` + where + `
AND ul.duration_ms IS NOT NULL
GROUP BY 1
ORDER BY 1 ASC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// Release the grouping cursor after success or early error; close failure is
	// ignored while scan and iteration errors prevent response assembly.
	defer func() { _ = rows.Close() }()

	labels := latencyHistogramBucketLabels(boundaries)
	counts := make(map[int]int64, len(labels))
	var total int64
	for rows.Next() {
		var bucketIndex int
		var count int64
		if err := rows.Scan(&bucketIndex, &count); err != nil {
			return nil, err
		}
		if bucketIndex < 0 || bucketIndex >= len(labels) {
			return nil, fmt.Errorf("invalid latency bucket index %d", bucketIndex)
		}
		counts[bucketIndex] = count
		total += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Restore each configured display bucket even when SQL returns no row for it,
	// retaining zero gaps rather than shortening the response's range list.
	buckets := make([]*ops.OpsLatencyHistogramBucket, 0, len(labels))
	for index, label := range labels {
		buckets = append(buckets, &ops.OpsLatencyHistogramBucket{
			Range: label,
			Count: counts[index],
		})
	}

	return &ops.OpsLatencyHistogramResponse{
		StartTime:          start,
		EndTime:            end,
		Platform:           strings.TrimSpace(filter.Platform),
		GroupID:            filter.GroupID,
		BucketBoundariesMS: boundaries,
		TotalRequests:      total,
		Buckets:            buckets,
	}, nil
}

// latencyHistogramBucketIndexCaseExpr builds ordered less-than branches from
// trusted column text and consecutive placeholder numbers. Callers supply a
// positive first placeholder, positive boundary count, and matching ordered
// arguments; equality proceeds to the following branch or final open bucket.
func latencyHistogramBucketIndexCaseExpr(column string, firstPlaceholder, boundaryCount int) string {
	var sb strings.Builder
	_, _ = sb.WriteString("CASE\n")

	for index := range boundaryCount {
		fmt.Fprintf(&sb, "\tWHEN %s < $%d THEN %d\n", column, firstPlaceholder+index, index)
	}

	fmt.Fprintf(&sb, "\tELSE %d\n", boundaryCount)
	_, _ = sb.WriteString("END")
	return sb.String()
}

// latencyHistogramBucketLabels formats supplied cutoff order as adjacent
// millisecond ranges plus a final open range. It performs no validation or
// sorting; empty input returns the single zero-plus label, and callers
// normalize production boundaries first.
func latencyHistogramBucketLabels(boundaries []int64) []string {
	labels := make([]string, 0, len(boundaries)+1)
	lower := int64(0)
	for _, upper := range boundaries {
		labels = append(labels, fmt.Sprintf("%d-%dms", lower, upper))
		lower = upper
	}
	labels = append(labels, fmt.Sprintf("%dms+", lower))
	return labels
}
