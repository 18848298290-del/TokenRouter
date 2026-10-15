package postgres

import (
	"context"
	"database/sql"

	"github.com/TokenFlux/TokenRouter/internal/team"
	"github.com/TokenFlux/TokenRouter/internal/usage/postgres/query"
)

// team_reports.go selects analytics or raw usage sources for team summaries and
// member histories.
// Team reports attribute usage to the acting user and group it into dates using
// the injected Calendar. Hourly UTC rows can represent whole local dates when
// each offset in the range is an integer number of hours. A fractional-hour
// offset splits a stored UTC hour at local midnight, so that range uses raw
// timestamps.
//
// The initial raw minimum matching team, actor, key, and range determines the
// source start. A missing minimum produces a successful empty source even when
// rollups remain. That lookup and the later report read are separate SQL
// statements whose consistency follows the selected executor. Summary and
// member-series readers retry the complete raw query after an analytics
// failure, while shared helpers supply daily totals and current or historical
// member identities.

// teamReportSource returns a scoped team source and whether analytics can
// answer the supplied query. Disabled aggregation and any fractional-hour
// timezone offset in the range select raw by returning false. The query's
// positive actor and key filters are passed to the shared source builder.
//
// The initial raw-history minimum supplies the lower bound, excluding older
// rollup-only history. When that lookup finds no matching record, it returns a
// usable empty CTE. Hourly rollups and raw edges retain user identities so
// shared helpers can regroup their timestamps into local report dates.
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
	// Check every offset period, including transitions within the range. A
	// fractional-hour offset puts local midnight inside a UTC bucket, requiring
	// raw timestamps for daily attribution.
	for cursor := request.From; cursor.Before(request.To); {
		local := cursor.In(r.calendar.Location())
		_, offset := local.Zone()
		// Zone offsets use seconds; 3600 seconds form one hour.
		if offset%3600 != 0 {
			return query.TeamReportSource{}, false, nil
		}
		_, next := local.ZoneBounds()
		if next.IsZero() || !next.Before(request.To) {
			break
		}
		cursor = next
	}

	// Rollup retention can outlast raw retention. Find the earliest raw record
	// under these same filters so report history begins with data still retained.
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

// GetTeamUsageSummary returns team totals and daily rows using the injected
// Calendar and request filters. Callers authorize team and actor scope and
// supply a valid [From, To) range; unavailable or failed analytics retries the
// complete raw source. Raw-query errors propagate.
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
	// Unavailable coverage and query errors select a complete filtered raw read.
	// Start from the original request so earlier analytics rows cannot be counted
	// again alongside that read.
	if err != nil {
		r.logUsageAnalyticsFallback("team_summary", err)
	}
	return query.UsageSummaryFromSource(ctx, r.sql, query.RawTeamReportSource(teamID, request), r.calendar)
}

// ListTeamMemberUsageSeries returns current members plus historical actors with
// matching usage, grouped into Calendar dates. Actor and key filters apply
// through the shared query helpers. Callers authorize their team/actor scope
// and supply a valid [From, To) range. Unavailable or failed analytics retries
// the complete raw query; current members without usage retain zero summaries.
func (r *Store) ListTeamMemberUsageSeries(ctx context.Context, teamID int64, request team.TeamUsageQuery) ([]team.TeamMemberUsageSeries, error) {
	source, ok, err := r.teamReportSource(ctx, teamID, request)
	if err == nil && ok {
		result, readErr := query.MemberUsageSeriesFromSource(ctx, r.sql, source, teamID, request, r.calendar)
		if readErr == nil {
			return result, nil
		}
		err = readErr
	}
	// Unavailable coverage and query errors select a complete filtered raw read.
	// Start from the original request so earlier analytics rows cannot be counted
	// again alongside that read.
	if err != nil {
		r.logUsageAnalyticsFallback("team_member_series", err)
	}
	return query.MemberUsageSeriesFromSource(ctx, r.sql, query.RawTeamReportSource(teamID, request), teamID, request, r.calendar)
}
