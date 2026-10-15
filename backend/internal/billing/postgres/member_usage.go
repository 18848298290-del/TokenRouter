package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// member_usage.go resets selected team-member usage windows to calendar
// starts.
// Team member quotas use calendar days, Monday weeks and calendar months in
// the configured location. Administrator resets derive all three starts from
// the same supplied instant, then update only selected usage windows on an
// active membership with the member role.
//
// The injected calendar determines those dates; without it, time.Local
// supplies calendar rules. This adapter runs one statement on its bound
// database rather than joining a context-carried Ent transaction. It
// interprets a zero affected count as missing membership, while the
// RowsAffected error is discarded and its companion count still controls that
// result.

// MemberUsageStore resets selected active member usage windows using an
// optional calendar.
type MemberUsageStore struct {
	db *sql.DB // bound member-update SQL database
	// calendar selects membership date windows when supplied. A nil pointer
	// selects process-local calendar rules at each reset, and callers keep a
	// supplied calendar stable during use.
	calendar *timezone.Calendar // optional member quota calendar
}

// NewMemberUsageStore binds a caller-owned database and optional calendar for
// membership resets. A nil calendar selects time.Local during each reset; the
// supplied database remains responsible for execution and any transaction
// lifecycle.
func NewMemberUsageStore(db *sql.DB, calendar *timezone.Calendar) *MemberUsageStore {
	return &MemberUsageStore{db: db, calendar: calendar}
}

// ResetMemberUsage zeros selected day/week/month amounts and aligns their
// starts to now in the configured calendar. The target must be an active
// role=member row; the bound database executes the statement, and a zero
// affected count returns ErrTeamMembershipRequired.
//
// Unselected windows retain amounts and starts, while updated_at advances on
// every matched update. Day, Monday-week and month starts share the supplied
// observation so crossing UTC dates cannot assign different calendar periods.
// The RowsAffected error is discarded, with its companion count used for
// missing-membership detection.
func (r *MemberUsageStore) ResetMemberUsage(ctx context.Context, teamID, userID int64, resetDaily, resetWeekly, resetMonthly bool, now time.Time) error {
	calendar := timezone.NewCalendar(time.Local)
	if r.calendar != nil {
		calendar = *r.calendar
	}
	dailyStart := calendar.StartOfDay(now)
	weeklyStart := calendar.StartOfWeek(now)
	monthlyStart := calendar.StartOfMonth(now)
	result, err := r.db.ExecContext(ctx, `
		UPDATE team_memberships SET
			daily_usage_usd = CASE WHEN $3 THEN 0 ELSE daily_usage_usd END,
			weekly_usage_usd = CASE WHEN $4 THEN 0 ELSE weekly_usage_usd END,
			monthly_usage_usd = CASE WHEN $5 THEN 0 ELSE monthly_usage_usd END,
			daily_window_start = CASE WHEN $3 THEN $6 ELSE daily_window_start END,
			weekly_window_start = CASE WHEN $4 THEN $7 ELSE weekly_window_start END,
			monthly_window_start = CASE WHEN $5 THEN $8 ELSE monthly_window_start END,
			updated_at = NOW()
		WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL AND role = 'member'`,
		teamID, userID, resetDaily, resetWeekly, resetMonthly, dailyStart, weeklyStart, monthlyStart)
	if err != nil {
		return err
	}
	// RowsAffected errors are discarded while retaining the returned count; only
	// a zero companion count selects the missing-membership error for this reset.
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return billing.ErrTeamMembershipRequired
	}
	return nil
}
