//go:build integration

// Collector and dashboard queries must count the same error categories.

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

func TestErrorCountsCollectorAndDashboard(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2038, 3, 10, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE created_at >= $1 AND created_at <= $2`, start, end)
		require.NoError(t, err)
	})
	for _, row := range []struct {
		status, upstream int
		owner, platform  string
		business, count  bool
		atEnd            bool
	}{
		{400, 0, "client", "openai", false, false, false},
		{500, 500, "provider", "openai", true, false, false},
		{500, 500, "provider", "openai", false, false, false},
		{429, 429, "provider", "openai", false, false, false},
		{529, 529, "provider", "openai", false, false, false},
		{500, 0, "platform", "openai", false, false, false},
		{503, 503, "provider", "anthropic", false, false, false},
		{503, 503, "provider", "openai", false, true, false},
		{503, 503, "provider", "openai", false, false, true},
	} {
		at := start.Add(time.Minute)
		if row.atEnd {
			at = end
		}
		var upstream any
		if row.upstream != 0 {
			upstream = row.upstream
		}
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs
			(error_phase,error_type,status_code,upstream_status_code,error_owner,platform,is_business_limited,is_count_tokens,created_at)
			VALUES('upstream','fixture',$1,$2,$3,$4,$5,$6,$7)`, row.status, upstream, row.owner, row.platform, row.business, row.count, at)
		require.NoError(t, err)
	}
	collector := &MetricsQueries{db: integrationDB}
	a, b, c, d, e, f, err := collector.QueryErrorCounts(ctx, start, end, []int{400})
	require.NoError(t, err)
	require.Equal(t, [6]int64{7, 2, 5, 2, 1, 1}, [6]int64{a, b, c, d, e, f})
	store := NewOpsRepository(integrationDB)
	for _, tc := range []struct {
		name     string
		platform string
		start    time.Time
		want     [6]int64
	}{
		{"global", "", start, [6]int64{7, 2, 5, 2, 1, 1}},
		{"platform", "openai", start, [6]int64{6, 2, 4, 1, 1, 1}},
		{"empty", "", start.Add(2 * time.Minute), [6]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c, d, e, f, err := store.queryErrorCounts(ctx, &ops.OpsDashboardFilter{Platform: tc.platform, IgnoredStatusCodes: []int{400}}, tc.start, end)
			require.NoError(t, err)
			require.Equal(t, tc.want, [6]int64{a, b, c, d, e, f})
		})
	}
}
