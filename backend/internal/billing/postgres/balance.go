package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Balance mutations distinguish general credits, redemption floors and
// administrator adjustments. Positive UpdateBalance credits also increase
// cumulative recharges used by percentage notifications; AddBalance and the
// raw redemption-floor statement change funds through their own rules. Methods
// select an Ent transaction from context when present.
//
// Administrator replacement captures the prior balance under a row lock in the
// same statement as the write. Delta adjustment checks the stored amount and
// reconstructs its returned previous value from the resulting amount. A failed
// delta condition uses a later read to distinguish missing users from
// insufficient funds, so that error companion is a current observation rather
// than the rejected statement's locked snapshot.

// BalanceStore applies billing fund mutations through a bound or
// context-carried Ent client.
type BalanceStore struct {
	// client is the default Ent connection. Methods prefer a transaction carried
	// by ctx, and the caller owns the selected connection's lifecycle.
	client *dbent.Client // default balance persistence client
}

// NewBalanceStore binds a caller-owned Ent client for balance operations.
// Context-carried transactions take precedence in its mutation/read methods;
// the store starts no transaction and leaves commit, rollback and publication
// to the caller.
func NewBalanceStore(client *dbent.Client) *BalanceStore { return &BalanceStore{client: client} }

// UpdateBalance adds amount to the matched user balance and adds positive
// amounts to total recharges. It uses the context transaction when present and
// returns ErrUserNotFound for no matched row; the ordinary Ent update supplies
// no explicit deleted-user predicate.
func (r *BalanceStore) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	client := clientFromContext(ctx, r.client)
	update := client.User.Update().Where(dbuser.IDEQ(id)).AddBalance(amount)
	// Positive credits increase the recharge base used by percentage balance
	// alerts; negative adjustments change funds while retaining that cumulative
	// credited amount.
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

// AddBalance adds amount to the matched user balance without changing
// cumulative recharges. Zero returns before persistence; the context
// transaction takes precedence, and no matched row returns ErrUserNotFound.
// The caller validates the amount.
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

// ApplyRedeemBalanceAdjustment applies delta to a non-deleted user and floors
// the result at zero in one SQL update. It uses the context transaction when
// present, leaves total recharges unchanged and returns ErrUserNotFound when
// no row is affected.
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

// AdjustBalance applies delta when the current non-deleted user balance would
// remain nonnegative, returning the SQL-reported old/new pair. The caller
// supplies a finite amount; SQL reconstructs Old as the returned stored New
// minus delta, subject to the balance column's numeric precision.
//
// A zero-row result can mean absence or insufficient funds. A subsequent
// balance read distinguishes them and supplies the ErrBalanceNegative
// companion values; concurrent changes can make that read newer than the
// rejected update. All operations use the context transaction when present.
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

	// A rejected update has no returning row; reread the current non-deleted
	// balance to distinguish absence from the funds condition and provide the
	// caller's error details.
	current, err := r.currentBalance(ctx, id)
	if err != nil {
		return billing.BalanceChange{}, err
	}
	return billing.BalanceChange{Old: current, New: current + delta}, billing.ErrBalanceNegative
}

// SetBalance replaces a non-deleted user balance and returns its locked prior
// and stored new amounts. A negative request is rejected after reading the
// current amount for its ErrBalanceNegative result, and an absent user returns
// ErrUserNotFound. Callers supply finite values.
//
// The successful path selects and locks the prior row in a MATERIALIZED CTE,
// then updates that same row and returns both observations. This prevents an
// earlier caller snapshot from overwriting the previous-value report when
// another billing write races with replacement.
func (r *BalanceStore) SetBalance(ctx context.Context, id int64, value float64) (billing.BalanceChange, error) {
	if value < 0 {
		// A negative replacement is rejected before updating; read the current
		// balance to report the requested and observed amounts to administrator
		// validation.
		current, err := r.currentBalance(ctx, id)
		if err != nil {
			return billing.BalanceChange{}, err
		}
		return billing.BalanceChange{Old: current, New: value}, billing.ErrBalanceNegative
	}
	// Lock the prior row before replacement and return it with the new stored
	// amount, keeping the administrator change report tied to this atomic update.
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

// currentBalance reads a non-deleted user's balance through the
// context-selected client. Absence returns ErrUserNotFound;
// query/scan/iteration errors propagate, and a row-close failure replaces the
// result error only when no earlier error exists.
func (r *BalanceStore) currentBalance(ctx context.Context, id int64) (balance float64, err error) {
	rows, err := clientFromContext(ctx, r.client).QueryContext(ctx,
		`SELECT balance FROM users WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return 0, err
	}
	// Close the row resource after decoding; retain any query/scan/iteration
	// error, and use a close failure only when it is the first failure.
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

// scanBalanceChange executes caller SQL returning old and new amounts through
// the supplied client. An empty successful result returns ok=false; scan and
// iteration errors propagate, while a close failure is returned only when an
// earlier error is absent.
func scanBalanceChange(ctx context.Context, client *dbent.Client, query string, args ...any) (change billing.BalanceChange, ok bool, err error) {
	rows, err := client.QueryContext(ctx, query, args...)
	if err != nil {
		return billing.BalanceChange{}, false, err
	}
	// Close the row resource after decoding; retain any query/scan/iteration
	// error, and use a close failure only when it is the first failure.
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

// CreditAffiliateTransfer delegates a transfer credit through the
// context-selected transaction client. Positive amounts also increase
// cumulative recharges using UpdateBalance, so percentage-based notification
// thresholds include this credited transfer; callers own the transaction.
func (r *BalanceStore) CreditAffiliateTransfer(ctx context.Context, id int64, amount float64) error {
	return r.UpdateBalance(ctx, id, amount)
}

// CreditRegistrationPromo delegates the signup credit through the
// context-selected transaction client. Positive amounts also increase
// cumulative recharges through UpdateBalance, and callers retain transaction
// ownership and post-commit notification responsibilities.
func (r *BalanceStore) CreditRegistrationPromo(ctx context.Context, id int64, amount float64) error {
	return r.UpdateBalance(ctx, id, amount)
}

// ApplyInitialUserFunds sets the initial balance on the supplied user creation
// builder. The caller provides a non-nil builder and owns persistence; this
// helper stages the balance without writing cumulative recharge data.
func ApplyInitialUserFunds(create *dbent.UserCreate, funds billing.InitialUserFunds) {
	create.SetBalance(funds.Balance)
}
