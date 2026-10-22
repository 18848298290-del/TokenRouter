package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestProviderUsageParticipantIncrement_ReturnedTotal selects missing rows and
// total-limit transitions from the participant's two-column result. It checks
// parameter order and leaves transaction completion with the caller.
func TestProviderUsageParticipantIncrement_ReturnedTotal(t *testing.T) {
	for _, tc := range []struct {
		name    string  // returned total scenario
		missing bool    // omit provider result
		used    float64 // returned total usage
		limit   float64 // returned total ceiling
		crossed bool    // expected crossing result
	}{
		{name: "missing provider", missing: true},
		{name: "below limit", used: 9, limit: 10},
		{name: "reaches limit", used: 10, limit: 10, crossed: true},
		{name: "crosses limit", used: 11, limit: 10, crossed: true},
		{name: "already exhausted", used: 12, limit: 10},
		{name: "unlimited", used: 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })

			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			rows := sqlmock.NewRows([]string{"quota_used", "quota_limit"})
			if !tc.missing {
				rows.AddRow(tc.used, tc.limit)
			}
			query := `(?s)^UPDATE providers SET extra = .*WHERE id = \$2 AND deleted_at IS NULL RETURNING ` +
				regexp.QuoteMeta(`COALESCE((extra->>'quota_used')::numeric, 0), COALESCE((extra->>'quota_limit')::numeric, 0)`) + `$`
			mock.ExpectQuery(query).
				WithArgs(2.0, int64(41)).
				WillReturnRows(rows).
				RowsWillBeClosed()
			mock.ExpectRollback()
			mock.ExpectClose()

			crossed, err := ProviderUsageInTx(tx).Increment(context.Background(), 41, 2)
			require.NoError(t, err)
			require.Equal(t, tc.crossed, crossed)
			// Complete the caller-owned transaction after reading the result. Any
			// participant commit or rollback would consume this expectation early.
			require.NoError(t, tx.Rollback())
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
