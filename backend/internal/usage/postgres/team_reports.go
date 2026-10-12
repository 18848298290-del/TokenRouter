package postgres

import (
	"context"
	"database/sql"

	"github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
)

// teamReportSource combines raw ranges and hourly rollups for grouping by local report dates.
func (r *Store) teamReportSource(ctx context.Context, teamID int64, request team.TeamUsageQuery) (query.TeamReportSource, bool, error) {
	if r.preAggregation == nil || !r.preAggregation.UsageEnabled(ctx) {
		return query.TeamReportSource{}, false, nil
	}
	filters := UsageLogFilters{TeamID: teamID}
	if request.ActorUserID != nil {
		filters.UserID = *request.ActorUserID
	}
	if request.APIKeyID != nil {
		filters.APIKeyID = *request.APIKeyID
	}
	// Fractional-hour timezone offsets split UTC buckets at midnight; use raw timestamps for those ranges.
	for cursor := request.From; cursor.Before(request.To); {
		local := cursor.In(r.calendar.Location())
		_, offset := local.Zone()
		if offset%3600 != 0 {
			return query.TeamReportSource{}, false, nil
		}
		_, next := local.ZoneBounds()
		if next.IsZero() || !next.Before(request.To) {
			break
		}
		cursor = next
	}

	// Rollups can outlive raw records; start from the oldest retained record matching this query.
	where, args := query.TeamUsageWhere(teamID, request)
	var oldest sql.NullTime
	if err := scanSingleRow(ctx, r.sql, "SELECT MIN(ul.created_at) FROM usage_logs ul WHERE "+where, args, &oldest); err != nil {
		return query.TeamReportSource{}, false, err
	}
	if !oldest.Valid {
		return query.TeamReportSource{CTE: `WITH scoped AS (
			SELECT NULL::bigint AS user_id,NULL::timestamptz AS created_at,0::numeric AS actual_cost,
			0::bigint AS total_requests,0::bigint AS input_tokens,0::bigint AS output_tokens WHERE FALSE)`}, true, nil
	}
	combined, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, oldest.Time, request.To, false)
	if err != nil || !ok {
		return query.TeamReportSource{}, false, err
	}
	return query.TeamReportSource{
		CTE: combined.cte + `, scoped AS NOT MATERIALIZED (
			SELECT user_id,occurred_at AS created_at,actual_cost,total_requests,input_tokens,output_tokens
			FROM combined ` + combined.where + `)`,
		Args: combined.args,
	}, true, nil
}

// GetTeamUsageSummary uses available rollups and retries the full raw query when aggregation fails.
// @project-doc docs/operations/pre_aggregation.md#query_routing_and_fallback
func (r *Store) GetTeamUsageSummary(ctx context.Context, teamID int64, request team.TeamUsageQuery) (*team.TeamUsageSummary, error) {
	source, ok, err := r.teamReportSource(ctx, teamID, request)
	if err == nil && ok {
		result, readErr := query.UsageSummaryFromSource(ctx, r.sql, source, r.calendar)
		if readErr == nil {
			return result, nil
		}
		err = readErr
	}
	if err != nil {
		r.logUsageAnalyticsFallback("team_summary", err)
	}
	return query.UsageSummaryFromSource(ctx, r.sql, query.RawTeamReportSource(teamID, request), r.calendar)
}

// ListTeamMemberUsageSeries includes current members and former members with usage in the range.
func (r *Store) ListTeamMemberUsageSeries(ctx context.Context, teamID int64, request team.TeamUsageQuery) ([]team.TeamMemberUsageSeries, error) {
	source, ok, err := r.teamReportSource(ctx, teamID, request)
	if err == nil && ok {
		result, readErr := query.MemberUsageSeriesFromSource(ctx, r.sql, source, teamID, request, r.calendar)
		if readErr == nil {
			return result, nil
		}
		err = readErr
	}
	if err != nil {
		r.logUsageAnalyticsFallback("team_member_series", err)
	}
	return query.MemberUsageSeriesFromSource(ctx, r.sql, query.RawTeamReportSource(teamID, request), teamID, request, r.calendar)
}
