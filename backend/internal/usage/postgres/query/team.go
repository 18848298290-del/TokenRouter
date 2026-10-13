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

// Team usage reports select recorded actor and key ownership within the caller's
// time range. Member reports combine current memberships with actors found in
// daily usage, so a departed member can remain visible and a current member can
// receive an empty summary.
//
// The scoped SQL source carries its own parameters and can represent raw rows or
// compatible aggregates. GROUPING SETS obtains totals and local-date rows in one
// scan, while member histories reuse the daily scan to identify historical
// actors. Calendar supplies the date-grouping timezone. These helpers require
// their caller to authorize the team and validate pagination and selectors
// before executing SQL.

// TeamReportSource carries a scoped SQL CTE and its bound argument list. Callers
// authorize the represented team and keep the CTE's positional columns
// compatible with the summary or member-series consumer.
type TeamReportSource struct {
	// CTE and Args form one SQL source: placeholders in CTE refer to Args in order.
	// Consumers copy the argument slice before appending their own timezone and
	// team parameters.
	CTE  string // scoped source SQL
	Args []any  // ordered source parameters
}

// TeamExecutor runs team reads on the caller's SQL connection or transaction.
type TeamExecutor interface {
	infra.Executor // statement and rows executor
	// QueryRowContext returns a single-row scanner on the caller's chosen
	// connection. The caller supplies context and bound parameters; Scan reports
	// query and conversion errors.
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ListUsageLogs counts and lists team usage in the supplied half-open range. The
// caller authorizes teamID and supplies validated limit/offset values; rows use
// descending creation time and ID, with current user email and key name for
// presentation.
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
	// Release listing rows after normal scanning or an early error. Close failures
	// are ignored here, while scan and iteration failures discard the partial list.
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

// RawTeamReportSource constructs a raw scoped CTE and its time/team parameters.
// Each retained usage row contributes one request, and positive actor or key
// selectors narrow the source under the caller's authorized team.
func RawTeamReportSource(teamID int64, query team.TeamUsageQuery) TeamReportSource {
	where, args := TeamUsageWhere(teamID, query)
	return TeamReportSource{CTE: `WITH scoped AS NOT MATERIALIZED (
 SELECT ul.user_id,ul.created_at,ul.actual_cost,1::bigint AS total_requests,ul.input_tokens,ul.output_tokens
 FROM usage_logs ul WHERE ` + where + `)`, Args: args}
}

// ListMemberUsageSeries returns current and historical member usage from raw
// records. The caller authorizes the team and passes its reporting Calendar;
// query's positive actor selector also limits current memberships.
func ListMemberUsageSeries(ctx context.Context, db TeamExecutor, teamID int64, query team.TeamUsageQuery, calendar timezone.Calendar) ([]team.TeamMemberUsageSeries, error) {
	return MemberUsageSeriesFromSource(ctx, db, RawTeamReportSource(teamID, query), teamID, query, calendar)
}

// MemberUsageSeriesFromSource returns daily usage grouped by actor from a
// compatible scoped source. Source parameters are copied before appending
// Calendar's timezone and teamID, and the caller owns authorization and source
// construction.
//
// The materialized daily rows supply both historical actors and their totals. A
// union adds current members, including members with no usage; the actor
// selector narrows that membership branch. Status is determined from current
// membership, so historical usage survives departure while sums count only dates
// returned by the source.
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
	// Release report rows after the scan or an early error. Close failures are
	// ignored, while scan and iteration failures abort the report.
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

// GetUsageSummary returns team totals and daily user charges from raw records.
// The caller supplies an authorized team, half-open time range, and Calendar;
// the raw source applies positive actor and key selectors.
func GetUsageSummary(ctx context.Context, db TeamExecutor, teamID int64, query team.TeamUsageQuery, calendar timezone.Calendar) (*team.TeamUsageSummary, error) {
	return UsageSummaryFromSource(ctx, db, RawTeamReportSource(teamID, query), calendar)
}

// UsageSummaryFromSource reads compatible scoped rows into overall and daily
// totals. It copies the argument slice before adding Calendar's named timezone,
// leaving the source's parameter list available for reuse.
//
// GROUPING SETS emits one summary row with a null date and populated local-date
// rows from the same scan. The null-date row supplies all token and request
// totals, while daily entries carry charges and request counts. This distinction
// keeps the overall sum separate from per-date display values, including an
// empty source's zero summary.
func UsageSummaryFromSource(ctx context.Context, db infra.Executor, source TeamReportSource, calendar timezone.Calendar) (*team.TeamUsageSummary, error) {
	args := append(append([]any{}, source.Args...), calendar.Location().String())
	rows, err := db.QueryContext(ctx, source.CTE+fmt.Sprintf(`
 SELECT TO_CHAR(created_at AT TIME ZONE $%d,'YYYY-MM-DD') AS day,
 COALESCE(SUM(actual_cost),0),COALESCE(SUM(total_requests),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0)
 FROM scoped GROUP BY GROUPING SETS ((1),()) ORDER BY day NULLS LAST`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	// Release report rows after the scan or an early error. Close failures are
	// ignored, while scan and iteration failures abort the report.
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

// TeamUsageWhere returns a team predicate and parameters for [From, To).
// Positive actor and key selectors append restrictions; teamID and timestamps
// are bound as supplied, so the caller performs validation and authorization.
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
