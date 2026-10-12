package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// BalanceStore applies atomic balance changes using the transaction in context when present.
type BalanceStore struct{ client *dbent.Client }

func NewBalanceStore(client *dbent.Client) *BalanceStore { return &BalanceStore{client: client} }
func (r *BalanceStore) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	client := clientFromContext(ctx, r.client)
	update := client.User.Update().Where(dbuser.IDEQ(id)).AddBalance(amount)
	// Track cumulative recharge amount for percentage-based notifications
	if amount > 0 {
		update = update.AddTotalRecharged(amount)
	}
	n, err := update.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, billing.ErrUserNotFound, nil)
	}
	if n == 0 {
		return billing.ErrUserNotFound
	}
	return nil
}

func (r *BalanceStore) AddBalance(ctx context.Context, id int64, amount float64) error {
	if amount == 0 {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	n, err := client.User.Update().
		Where(dbuser.IDEQ(id)).
		AddBalance(amount).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, billing.ErrUserNotFound, nil)
	}
	if n == 0 {
		return billing.ErrUserNotFound
	}
	return nil
}

// ApplyRedeemBalanceAdjustment atomically applies a redemption delta and clamps the balance to zero.
func (r *BalanceStore) ApplyRedeemBalanceAdjustment(ctx context.Context, id int64, delta float64) error {
	const updateSQL = `
		UPDATE users
		SET balance = GREATEST(balance + $1, 0), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, updateSQL, delta, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return billing.ErrUserNotFound
	}
	return nil
}

// AdjustBalance atomically adds delta and rejects changes that would make the balance negative.
// The UPDATE uses the current stored balance so concurrent billing deductions
// cannot be overwritten by a stale user snapshot.
func (r *BalanceStore) AdjustBalance(ctx context.Context, id int64, delta float64) (billing.BalanceChange, error) {
	const updateSQL = `
		UPDATE users
		SET balance = balance + $1, updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND balance + $1 >= 0
		RETURNING balance - $1, balance
	`
	change, ok, err := scanBalanceChange(ctx, clientFromContext(ctx, r.client), updateSQL, delta, id)
	if err != nil {
		return billing.BalanceChange{}, err
	}
	if ok {
		return change, nil
	}

	// No matched row can mean a missing user or insufficient balance; distinguish them with a read.
	current, err := r.currentBalance(ctx, id)
	if err != nil {
		return billing.BalanceChange{}, err
	}
	return billing.BalanceChange{Old: current, New: current + delta}, billing.ErrBalanceNegative
}

// SetBalance atomically replaces the balance and returns its previous and new values.
func (r *BalanceStore) SetBalance(ctx context.Context, id int64, value float64) (billing.BalanceChange, error) {
	if value < 0 {
		// Return the current and requested balances so the caller can explain the rejection.
		current, err := r.currentBalance(ctx, id)
		if err != nil {
			return billing.BalanceChange{}, err
		}
		return billing.BalanceChange{Old: current, New: value}, billing.ErrBalanceNegative
	}
	const updateSQL = `
		WITH prev AS MATERIALIZED (
            SELECT id, balance FROM users
            WHERE id = $2 AND deleted_at IS NULL
            FOR NO KEY UPDATE
        )
        UPDATE users AS u
        SET balance = $1, updated_at = NOW()
        FROM prev
        WHERE u.id = prev.id AND u.deleted_at IS NULL
        RETURNING prev.balance, u.balance
	`
	change, ok, err := scanBalanceChange(ctx, clientFromContext(ctx, r.client), updateSQL, value, id)
	if err != nil {
		return billing.BalanceChange{}, err
	}
	if !ok {
		return billing.BalanceChange{}, billing.ErrUserNotFound
	}
	return change, nil
}

// currentBalance reads the current balance and returns ErrUserNotFound for a missing user.
func (r *BalanceStore) currentBalance(ctx context.Context, id int64) (balance float64, err error) {
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx,
		`SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if rowsErr := rows.Err(); rowsErr != nil {
			return 0, rowsErr
		}
		return 0, billing.ErrUserNotFound
	}
	if err := rows.Scan(&balance); err != nil {
		return 0, err
	}
	return balance, rows.Err()
}

// scanBalanceChange executes a statement returning old and new balances. A missing result returns ok=false.
func scanBalanceChange(ctx context.Context, client *dbent.Client, query string, args ...any) (change billing.BalanceChange, ok bool, err error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return billing.BalanceChange{}, false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if !rows.Next() {
		if rowsErr := rows.Err(); rowsErr != nil {
			return billing.BalanceChange{}, false, rowsErr
		}
		return billing.BalanceChange{}, false, nil
	}
	if err := rows.Scan(&change.Old, &change.New); err != nil {
		return billing.BalanceChange{}, false, err
	}
	return change, true, rows.Err()
}

// CreditAffiliateTransfer adds the transfer to balance and, for positive amounts, total recharged using the
// transaction in context.
func (r *BalanceStore) CreditAffiliateTransfer(ctx context.Context, id int64, amount float64) error {
	return r.UpdateBalance(ctx, id, amount)
}

// CreditRegistrationPromo adds a registration credit; positive amounts also increase total recharged.
func (r *BalanceStore) CreditRegistrationPromo(ctx context.Context, id int64, amount float64) error {
	return r.UpdateBalance(ctx, id, amount)
}

// ApplyInitialUserFunds sets the initial balance on a user creation builder.
func ApplyInitialUserFunds(create *dbent.UserCreate, funds billing.InitialUserFunds) {
	create.SetBalance(funds.Balance)
}
