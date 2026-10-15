package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// available_balance.go deducts refundable funds up to the positive stored
// balance.
// Refund preparation needs a deduction capped by the user's positive stored
// balance when the requested amount exceeds those funds. The SQL
// locks a non-deleted user, updates that same row and returns the deducted
// amount together with the remaining balance in one statement.
//
// An existing nonpositive balance remains unchanged and contributes no
// deduction. A positive balance smaller than the request is reduced to zero;
// otherwise the requested amount is removed. This calculation reads raw
// balance, with frozen funds handled by other billing paths. Callers supply a
// nonnegative amount and own any surrounding transaction.

// DeductAvailableBalance selects a deduction capped by positive raw balance
// and returns it with the stored new balance. The caller supplies a
// nonnegative finite amount compatible with stored numeric precision and a
// usable query connection; an absent non-deleted user returns ErrUserNotFound.
//
// The locked balance controls update and deduction selection in one SQL
// statement, preventing a stale caller snapshot from choosing the cap.
// Existing debt remains with zero deduction. The reported deduction is
// selected before column rounding, frozen balance is not an input, and the
// caller owns any surrounding transaction.
func DeductAvailableBalance(ctx context.Context, q postgresinfra.Queryer, userID int64, amount float64) (float64, float64, error) {
	const query = `
		WITH locked_user AS (
			SELECT id, balance
			FROM users
			WHERE id = $2
				AND deleted_at IS NULL
			FOR UPDATE
		), updated AS (
			UPDATE users
			SET balance = CASE
				WHEN locked_user.balance <= 0 THEN locked_user.balance
				WHEN locked_user.balance <= $1 THEN 0
				ELSE locked_user.balance - $1
			END,
				updated_at = NOW()
			FROM locked_user
			WHERE users.id = locked_user.id
			RETURNING users.balance
		)
		SELECT
			updated.balance,
			CASE
				WHEN locked_user.balance <= 0 THEN 0
				WHEN locked_user.balance <= $1 THEN locked_user.balance
				ELSE $1
			END AS deducted_amount
		FROM updated
		CROSS JOIN locked_user
	`

	// Select the deduction from the locked balance and return it with the stored
	// result; refund preparation receives both observations from this statement.
	var (
		newBalance     float64
		deductedAmount float64
	)
	if err := postgresinfra.ScanSingleRow(ctx, q, query, []any{amount, userID}, &newBalance, &deductedAmount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, billing.ErrUserNotFound
		}
		return 0, 0, err
	}
	return newBalance, deductedAmount, nil
}
