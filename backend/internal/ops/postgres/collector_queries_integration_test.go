//go:build integration

package postgres

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/timing"
)

// TestUsagePerformanceSQLMatchesRecordedMetrics compares PostgreSQL aggregation
// expressions with usage API derivation. Missing, zero, inverted, incomplete and
// differently selected observations must yield identical samples and rounded TPS.
func TestUsagePerformanceSQLMatchesRecordedMetrics(t *testing.T) {
	complete := timing.NewPerformance(timing.FirstOutput, 100)
	complete.Stream, complete.Complete, complete.UsageComplete = true, true, true
	complete.Values["upstream_first_event_ms"] = 100
	complete.Values["upstream_first_output_ms"] = 300
	complete.Values["upstream_last_output_ms"] = 900
	complete.Values["upstream_output_event_count"] = 2
	for _, tc := range []struct {
		name   string                    // independent sample label
		change func(*timing.Performance) // sample-specific observation change
	}{
		{"content", func(*timing.Performance) {}},
		{"zero_event", func(p *timing.Performance) { p.Mode = timing.FirstEvent }},
		{"missing_start", func(p *timing.Performance) { delete(p.Values, "upstream_started_ms") }},
		{"inverted", func(p *timing.Performance) { p.Values["upstream_first_output_ms"] = 90 }},
		{"single_chunk", func(p *timing.Performance) { p.Values["upstream_output_event_count"] = 1 }},
		{"partial_usage", func(p *timing.Performance) { p.UsageComplete = false }},
		{"nonstreaming", func(p *timing.Performance) { p.Stream = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := complete.Clone()
			tc.change(p)
			encoded, err := json.Marshal(p)
			require.NoError(t, err)
			var latency sql.NullInt64
			var speed sql.NullFloat64
			query := "SELECT " + usageTTFTSQL("u") + "," + usageTPSSQL("u") + " FROM (SELECT $1::jsonb AS timings,17::int AS output_tokens) u"
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), query, string(encoded)).Scan(&latency, &speed))
			require.Equal(t, p.TTFTMs() != nil, latency.Valid)
			if latency.Valid {
				require.Equal(t, int64(*p.TTFTMs()), latency.Int64)
			}
			require.Equal(t, p.TPS(17) != nil, speed.Valid)
			if speed.Valid {
				require.InDelta(t, *p.TPS(17), speed.Float64, 0.00001)
			}
		})
	}
}
