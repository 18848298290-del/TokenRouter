//go:build integration

package postgres

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Key dashboard totals combine retained usage, local today, nonnull duration
// averages, and recent raw rates. A Kathmandu fixture compares raw and analytics
// results with future records, then deletes older rows to check that stale
// rollup history does not enlarge the retained total.

// TestAPIKeyDashboardAnalyticsParity records usage around Kathmandu dates with
// null timings, zero costs, and future instants. It compares complete
// raw/analytics dashboards, then checks parity after retained raw history
// shrinks.
func (s *UsageLogRepoSuite) TestAPIKeyDashboardAnalyticsParity() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "key-analytics@test.local"})
	key := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "key-analytics"})
	p := mustCreateProvider(s.T(), s.client, &provider.Record{Name: "key-analytics"})
	loc, err := time.LoadLocation("Asia/Kathmandu")
	s.Require().NoError(err)
	s.repo.calendar = timezone.NewCalendar(loc)
	now := time.Now().UTC().Truncate(time.Second)
	for i, at := range []time.Time{now.Add(-120 * time.Hour), now.Add(-53 * time.Hour), now.Add(-12 * time.Hour), now.Add(-2 * time.Hour), now.Add(48 * time.Hour)} {
		log := s.createUsageLog(user, key, p, 10+i, 5+i, float64(i), at)
		if i%2 == 0 {
			_, err = s.tx.ExecContext(s.ctx, "UPDATE usage_logs SET duration_ms=$1 WHERE id=$2", i*100, log.ID)
			s.Require().NoError(err)
		}
	}
	want, err := s.repo.GetAPIKeyDashboardStats(s.ctx, key.ID)
	s.Require().NoError(err)
	start := now.Add(-72 * time.Hour).Truncate(time.Hour)
	watermark := now.Add(-3 * time.Hour)
	aggregation := NewAggregationStoreWithSQL(s.tx, s.repo.calendar)
	s.Require().NoError(aggregation.AggregateUsageAnalyticsRange(s.ctx, start, watermark))
	s.Require().NoError(aggregation.SaveUsageAnalyticsAggregationState(s.ctx, &usage.UsageAnalyticsAggregationState{
		LiveWatermark: watermark, CoverageStart: &start, Phase: "idle",
	}))
	s.repo.preAggregation = preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	_, ok, err := s.repo.getAPIKeyDashboardStatsFromAnalytics(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().True(ok)
	got, err := s.repo.GetAPIKeyDashboardStats(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().Equal(want, got)
	// Delete retained history without rebuilding old buckets, then compare readers;
	// the key's oldest remaining raw record must bound its retained analytics
	// total.
	_, err = s.tx.ExecContext(s.ctx, "DELETE FROM usage_logs WHERE api_key_id=$1 AND created_at<$2", key.ID, now.Add(-24*time.Hour))
	s.Require().NoError(err)
	got, err = s.repo.GetAPIKeyDashboardStats(s.ctx, key.ID)
	s.Require().NoError(err)
	s.repo.preAggregation = nil
	want, err = s.repo.GetAPIKeyDashboardStats(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().Equal(want, got)
}
