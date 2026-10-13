package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// Member calendar cases compare settlement.go accumulation and member_usage.go
// administrator reset arguments in the injected New York timezone.

// TestMemberWritesUseInjectedCalendar supplies two UTC observations that land
// on New York Sunday evenings near daylight-saving transitions. It checks
// settlement accumulation and administrator resets receive matching local day,
// Monday-week and month SQL arguments for those observations.
func TestMemberWritesUseInjectedCalendar(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	calendar := timezone.NewCalendar(location)
	for _, now := range []time.Time{
		time.Date(2026, 3, 9, 2, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 2, 30, 0, 0, time.UTC),
	} {
		t.Run(now.Format("2006-01-02"), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
			}()
			day := time.Date(now.In(location).Year(), now.In(location).Month(), now.In(location).Day(), 0, 0, 0, 0, location)
			// Both local observations are Sunday, so subtract six calendar days to
			// obtain the expected Monday start across their offset changes.
			week := day.AddDate(0, 0, -6)
			month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, location)
			// Exercise accumulation on a transaction, then roll that mock transaction
			// back before the standalone administrator reset uses the same calendar
			// argument expectations.
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			mock.ExpectExec("UPDATE team_memberships SET").WithArgs(int64(11), int64(22), 3.5, day, week, month, now).WillReturnResult(sqlmock.NewResult(0, 1))
			store := NewSettlementStore(db, calendar, nil)
			require.NoError(t, store.incrementUsageBillingTeamMember(context.Background(), tx, 11, 22, 3.5, now))
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			mock.ExpectExec("UPDATE team_memberships SET").WithArgs(int64(11), int64(22), true, true, true, day, week, month).WillReturnResult(sqlmock.NewResult(0, 1))
			require.NoError(t, NewMemberUsageStore(db, &calendar).ResetMemberUsage(context.Background(), 11, 22, true, true, true, now))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
