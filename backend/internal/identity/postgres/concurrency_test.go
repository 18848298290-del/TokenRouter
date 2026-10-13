package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// Redeem concurrency persistence uses a SQL expression that combines the
// signed delta with a zero floor. The mock case checks the complete
// expression, deleted-row filter, arguments, and affected-row result through
// the account repository facade.

// TestApplyRedeemConcurrencyAdjustment_UsesAtomicFloor sends a negative redeem
// delta through the repository facade. It checks the zero-floor SQL,
// deleted-row predicate, arguments, and completion of the mock expectation.
func TestApplyRedeemConcurrencyAdjustment_UsesAtomicFloor(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectExec(`UPDATE users SET concurrency = GREATEST\(concurrency \+ \$1, 0\), updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(-7, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.ApplyRedeemConcurrencyAdjustment(context.Background(), 42, -7))
	require.NoError(t, mock.ExpectationsWereMet())
}
