package query

import (
	"context"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/lib/pq"

	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// IdentityUserLastUsedAtOrder sorts by transactionally maintained activity, with missing users ordered as nulls.
// @project-doc docs/operations/observability_and_data_lifecycle.md#user_activity_summary
func IdentityUserLastUsedAtOrder(sortOrder string) []func(*entsql.Selector) {
	orderExpr := func(direction, nulls string, tieOrder func(string) string) func(*entsql.Selector) {
		return func(s *entsql.Selector) {
			activity := entsql.Table("usage_user_activity").As("user_activity")
			s.LeftJoin(activity).On(s.C(dbuser.FieldID), activity.C("user_id"))
			s.OrderExpr(entsql.Expr(activity.C("last_used_at") + " " + direction + " NULLS " + nulls))
			s.OrderBy(tieOrder(s.C(dbuser.FieldID)))
		}
	}

	if sortOrder == pagination.SortOrderAsc {
		return []func(*entsql.Selector){
			orderExpr("ASC", "FIRST", entsql.Asc),
		}
	}
	return []func(*entsql.Selector){
		orderExpr("DESC", "LAST", entsql.Desc),
	}
}

// GetLatestUsedAtByUserIDs reads activity in a batch and omits users with no activity row.
func GetLatestUsedAtByUserIDs(ctx context.Context, db infra.Executor, userIDs []int64) (map[int64]*time.Time, error) {
	result := make(map[int64]*time.Time, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	if db == nil {
		return nil, fmt.Errorf("sql executor is not configured")
	}

	const query = `
		SELECT user_id, last_used_at
		FROM usage_user_activity
		WHERE user_id = ANY($1)
	`

	rows, err := db.QueryContext(ctx, query, pq.Array(userIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			userID     int64
			lastUsedAt time.Time
		)
		if scanErr := rows.Scan(&userID, &lastUsedAt); scanErr != nil {
			return nil, scanErr
		}
		ts := lastUsedAt.UTC()
		result[userID] = &ts
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
