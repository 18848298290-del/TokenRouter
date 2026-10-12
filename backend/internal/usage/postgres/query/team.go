package query

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// TeamReportSource contains a team-filtered report source with bound parameters; callers authorize the team scope.
type TeamReportSource struct {
	CTE  string
	Args []any
}

// TeamExecutor uses the SQL connection supplied by the team caller.
type TeamExecutor interface {
	infra.Executor
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ListUsageLogs(ctx context.Context, db TeamExecutor, teamID int64, query team.TeamUsageQuery) ([]team.TeamUsageLogItem, int64, error) {
	where, args := TeamUsageWhere(teamID, query)
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_logs ul WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limitPos := len(args) + 1
	offsetPos := len(args) + 2
	args = append(args, query.Limit, query.Offset)
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT ul.id, ul.user_id, COALESCE(u.email, ''), ul.api_key_id, COALESCE(k.name, ''),
		       ul.request_id, COALESCE(NULLIF(ul.requested_model, ''), ul.model), ul.actual_cost,
		       ul.input_tokens, ul.output_tokens, ul.created_at
		FROM usage_logs ul
		LEFT JOIN users u ON u.id = ul.user_id
		LEFT JOIN api_keys k ON k.id = ul.api_key_id
		WHERE %s ORDER BY ul.created_at DESC, ul.id DESC LIMIT $%d OFFSET $%d`, where, limitPos, offsetPos), args...)
	if err != nil {
		return nil, 0, err
	}
	// Close the result set on every exit; iteration errors are checked through rows.Err below.
	defer func() { _ = rows.Close() }()
	items := make([]team.TeamUsageLogItem, 0, query.Limit)
	for rows.Next() {
		var item team.TeamUsageLogItem
		if err := rows.Scan(&item.ID, &item.ActorUserID, &item.ActorEmail, &item.APIKeyID, &item.APIKeyName, &item.RequestID, &item.Model, &item.ActualCost, &item.InputTokens, &item.OutputTokens, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// RawTeamReportSource supplies a raw-record source when rollups are unavailable or disabled.
func RawTeamReportSource(teamID int64, query team.TeamUsageQuery) TeamReportSource {
	where, args := TeamUsageWhere(teamID, query)
	return TeamReportSource{CTE: `WITH scoped AS NOT MATERIALIZED (
 SELECT ul.user_id,ul.created_at,ul.actual_cost,1::bigint AS total_requests,ul.input_tokens,ul.output_tokens
 FROM usage_logs ul WHERE ` + where + `)`, Args: args}
}

// ListMemberUsageSeries combines current membership with historical raw usage for member reports.
func ListMemberUsageSeries(ctx context.Context, db TeamExecutor, teamID int64, query team.TeamUsageQuery, calendar timezone.Calendar) ([]team.TeamMemberUsageSeries, error) {
	return MemberUsageSeriesFromSource(ctx, db, RawTeamReportSource(teamID, query), teamID, query, calendar)
}

// MemberUsageSeriesFromSource derives historical members from daily totals to avoid another raw scan.
func MemberUsageSeriesFromSource(ctx context.Context, db infra.Executor, source TeamReportSource, teamID int64, query team.TeamUsageQuery, calendar timezone.Calendar) ([]team.TeamMemberUsageSeries, error) {
	args := append(append([]any{}, source.Args...), calendar.Location().String(), teamID)
	tzPos, teamPos := len(args)-1, len(args)
	actorFilter := ""
	if query.ActorUserID != nil && *query.ActorUserID > 0 {
		args = append(args, *query.ActorUserID)
		actorFilter = fmt.Sprintf(" AND m.user_id=$%d", len(args))
	}
	rows, err := db.QueryContext(ctx, source.CTE+fmt.Sprintf(`,
 daily AS MATERIALIZED (
 SELECT user_id,TO_CHAR(created_at AT TIME ZONE $%d,'YYYY-MM-DD') AS usage_date,
 COALESCE(SUM(actual_cost),0) AS actual_cost,COALESCE(SUM(total_requests),0) AS request_count,
 COALESCE(SUM(input_tokens),0) AS input_tokens,COALESCE(SUM(output_tokens),0) AS output_tokens
 FROM scoped GROUP BY 1,2
 ),actors AS (
 SELECT m.user_id FROM team_memberships m WHERE m.team_id=$%d AND m.left_at IS NULL%s
 UNION SELECT user_id FROM daily
 )
 SELECT a.user_id,COALESCE(NULLIF(u.username,''),NULLIF(u.email,''),'User #'||a.user_id::text),
 CASE WHEN EXISTS(SELECT 1 FROM team_memberships m WHERE m.team_id=$%d AND m.user_id=a.user_id AND m.left_at IS NULL) THEN 'active' ELSE 'left' END,
 d.usage_date,COALESCE(d.actual_cost,0),COALESCE(d.request_count,0),COALESCE(d.input_tokens,0),COALESCE(d.output_tokens,0)
 FROM actors a LEFT JOIN users u ON u.id=a.user_id LEFT JOIN daily d ON d.user_id=a.user_id
 ORDER BY a.user_id,d.usage_date`, tzPos, teamPos, actorFilter, teamPos), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]team.TeamMemberUsageSeries, 0)
	indexByUser := make(map[int64]int)
	for rows.Next() {
		var id, requests, input, output int64
		var name, status string
		var date sql.NullString
		var cost float64
		if err := rows.Scan(&id, &name, &status, &date, &cost, &requests, &input, &output); err != nil {
			return nil, err
		}
		index, exists := indexByUser[id]
		if !exists {
			index = len(items)
			indexByUser[id] = index
			items = append(items, team.TeamMemberUsageSeries{ActorUserID: id, DisplayName: name, Status: status, Summary: team.TeamUsageSummary{Daily: make([]team.TeamUsageDaily, 0)}})
		}
		if date.Valid {
			summary := &items[index].Summary
			summary.Daily = append(summary.Daily, team.TeamUsageDaily{Date: date.String, ActualCost: cost, RequestCount: requests})
			summary.ActualCost += cost
			summary.RequestCount += requests
			summary.InputTokens += input
			summary.OutputTokens += output
		}
	}
	return items, rows.Err()
}

// GetUsageSummary computes totals and daily rows using database numeric sums.
func GetUsageSummary(ctx context.Context, db TeamExecutor, teamID int64, query team.TeamUsageQuery, calendar timezone.Calendar) (*team.TeamUsageSummary, error) {
	return UsageSummaryFromSource(ctx, db, RawTeamReportSource(teamID, query), calendar)
}

// UsageSummaryFromSource uses GROUPING SETS to produce totals and daily rows in one scan.
func UsageSummaryFromSource(ctx context.Context, db infra.Executor, source TeamReportSource, calendar timezone.Calendar) (*team.TeamUsageSummary, error) {
	args := append(append([]any{}, source.Args...), calendar.Location().String())
	rows, err := db.QueryContext(ctx, source.CTE+fmt.Sprintf(`
 SELECT TO_CHAR(created_at AT TIME ZONE $%d,'YYYY-MM-DD') AS day,
 COALESCE(SUM(actual_cost),0),COALESCE(SUM(total_requests),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0)
 FROM scoped GROUP BY GROUPING SETS ((1),()) ORDER BY day NULLS LAST`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := &team.TeamUsageSummary{Daily: make([]team.TeamUsageDaily, 0)}
	for rows.Next() {
		var date sql.NullString
		var cost float64
		var requests, input, output int64
		if err := rows.Scan(&date, &cost, &requests, &input, &output); err != nil {
			return nil, err
		}
		if date.Valid {
			result.Daily = append(result.Daily, team.TeamUsageDaily{Date: date.String, ActualCost: cost, RequestCount: requests})
		} else {
			result.ActualCost = cost
			result.RequestCount = requests
			result.InputTokens = input
			result.OutputTokens = output
		}
	}
	return result, rows.Err()
}

func TeamUsageWhere(teamID int64, query team.TeamUsageQuery) (string, []any) {
	conditions := []string{"ul.team_id = $1", "ul.created_at >= $2", "ul.created_at < $3"}
	args := []any{teamID, query.From, query.To}
	if query.ActorUserID != nil && *query.ActorUserID > 0 {
		args = append(args, *query.ActorUserID)
		conditions = append(conditions, fmt.Sprintf("ul.user_id = $%d", len(args)))
	}
	if query.APIKeyID != nil && *query.APIKeyID > 0 {
		args = append(args, *query.APIKeyID)
		conditions = append(conditions, fmt.Sprintf("ul.api_key_id = $%d", len(args)))
	}
	return strings.Join(conditions, " AND "), args
}
