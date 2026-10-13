//go:build integration

package postgres

import (
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Team reports combine membership with usage attributed to current and former
// actors. This fixture includes a current member with no usage, multiple keys,
// and records around report and aggregation endpoints. Raw and
// analytics-selected reports are compared in UTC, New York and Kathmandu,
// including Kathmandu's deliberate raw selection and the empty-history case
// after deletion.

// TestTeamReportsAnalyticsParity compares raw and selected report results
// across team, actor, key and local-date conditions. Fractional-hour Kathmandu
// selects raw, former actors retain history, current empty members remain, and
// deleting all raw history removes stale-rollup totals.
func (s *UsageLogRepoSuite) TestTeamReportsAnalyticsParity() {
	owner := mustCreateUser(s.T(), s.client, &identity.User{Email: "report-owner@test.local"})
	member := mustCreateUser(s.T(), s.client, &identity.User{Email: "report-member@test.local"})
	former := mustCreateUser(s.T(), s.client, &identity.User{Email: "report-former@test.local"})
	empty := mustCreateUser(s.T(), s.client, &identity.User{Email: "report-empty@test.local"})
	entity, err := s.client.Team.Create().SetName("报表团队").SetStatus("active").SetMemberLimit(10).Save(s.ctx)
	s.Require().NoError(err)
	for _, u := range []*identity.User{owner, member, empty} {
		role := team.TeamRoleMember
		if u.ID == owner.ID {
			role = team.TeamRoleOwner
		}
		_, err = s.client.TeamMembership.Create().SetTeamID(entity.ID).SetUserID(u.ID).SetRole(role).Save(s.ctx)
		s.Require().NoError(err)
	}
	a := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: owner.ID, TeamID: &entity.ID, Key: "report-a"})
	b := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: owner.ID, TeamID: &entity.ID, Key: "report-b"})
	p := mustCreateProvider(s.T(), s.client, &provider.Record{Name: "report"})
	start := time.Date(2026, 9, 1, 10, 20, 0, 0, time.UTC)
	end := start.Add(96 * time.Hour)
	// Use a rounded coverage start and a partial live watermark so raw edges
	// meet hourly rows at different minute positions in the parity comparisons.
	coverage := start.Add(12 * time.Hour).Truncate(time.Hour)
	watermark := end.Add(-2 * time.Hour)
	for i, actor := range []*identity.User{owner, member, former} {
		for j, at := range []time.Time{start.Add(-time.Second), start, start.Add(24 * time.Hour), watermark.Add(-time.Minute), watermark.Add(time.Minute), end} {
			keyID := a.ID
			if j%2 == 0 {
				keyID = b.ID
			}
			_, err = s.repo.Create(s.ctx, &usage.UsageLog{UserID: actor.ID, BillingUserID: owner.ID, TeamID: &entity.ID, APIKeyID: keyID, ProviderID: p.ID, RequestID: fmt.Sprintf("team-report-%d-%d", i, j), Model: "test", CreatedAt: at, InputTokens: 10, OutputTokens: 20, ActualCost: float64(j), TotalCost: float64(j)})
			s.Require().NoError(err)
		}
	}
	aggregation := NewAggregationStoreWithSQL(s.tx, s.repo.calendar)
	s.Require().NoError(aggregation.AggregateUsageAnalyticsRange(s.ctx, coverage, watermark))
	s.Require().NoError(aggregation.SaveUsageAnalyticsAggregationState(s.ctx, &usage.UsageAnalyticsAggregationState{CoverageStart: &coverage, LiveWatermark: watermark, Phase: "idle"}))
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true}})
	// Use an integer-offset DST zone and a fractional-hour zone alongside UTC; the
	// latter cannot assign whole UTC hours to local report dates.
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kathmandu"} {
		loc, err := time.LoadLocation(zone)
		s.Require().NoError(err)
		s.repo.calendar = timezone.NewCalendar(loc)
		for _, request := range []team.TeamUsageQuery{
			{From: start, To: end},
			{From: start, To: end, ActorUserID: &member.ID},
			{From: start, To: end, APIKeyID: &a.ID},
			{From: start, To: end, APIKeyID: &b.ID, ActorUserID: &member.ID},
		} {
			s.repo.preAggregation = nil
			want, err := s.repo.GetTeamUsageSummary(s.ctx, entity.ID, request)
			s.Require().NoError(err)
			members, err := s.repo.ListTeamMemberUsageSeries(s.ctx, entity.ID, request)
			s.Require().NoError(err)
			s.repo.preAggregation = settings
			_, ok, err := s.repo.teamReportSource(s.ctx, entity.ID, request)
			s.Require().NoError(err)
			s.Require().Equal(zone != "Asia/Kathmandu", ok)
			got, err := s.repo.GetTeamUsageSummary(s.ctx, entity.ID, request)
			s.Require().NoError(err)
			s.Require().Equal(want, got)
			gotMembers, err := s.repo.ListTeamMemberUsageSeries(s.ctx, entity.ID, request)
			s.Require().NoError(err)
			s.Require().Equal(members, gotMembers)
			if request.ActorUserID != nil {
				s.Require().Len(gotMembers, 1)
				s.Require().Equal(member.ID, gotMembers[0].ActorUserID)
			} else {
				s.Require().Len(gotMembers, 4)
				s.Require().Equal("left", gotMembers[2].Status)
				s.Require().Zero(gotMembers[3].Summary.RequestCount)
			}
		}
	}
	// Leave the previously built rollups in place while removing their raw source;
	// the final assertions exercise retained-history gating and membership-only
	// zero summaries.
	s.repo.calendar = timezone.NewCalendar(time.UTC)
	_, err = s.tx.ExecContext(s.ctx, "DELETE FROM usage_logs WHERE team_id=$1", entity.ID)
	s.Require().NoError(err)
	request := team.TeamUsageQuery{From: start, To: end}
	got, err := s.repo.GetTeamUsageSummary(s.ctx, entity.ID, request)
	s.Require().NoError(err)
	s.Require().Zero(got.RequestCount)
	members, err := s.repo.ListTeamMemberUsageSeries(s.ctx, entity.ID, request)
	s.Require().NoError(err)
	s.Require().Len(members, 3)
	for _, item := range members {
		s.Require().Zero(item.Summary.RequestCount)
	}
}
