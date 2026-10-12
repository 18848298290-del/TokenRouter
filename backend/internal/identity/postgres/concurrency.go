package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
)

// ConcurrencyStore updates user concurrency limits within caller-owned billing transactions.
type ConcurrencyStore struct{ client *dbent.Client }

func NewConcurrencyStore(client *dbent.Client) *ConcurrencyStore {
	return &ConcurrencyStore{client: client}
}

func (r *ConcurrencyStore) UpdateConcurrency(ctx context.Context, id int64, amount int) error {
	client := clientFromContext(ctx, r.client)
	n, err := client.User.Update().Where(dbuser.IDEQ(id)).AddConcurrency(amount).Save(ctx)
	if err != nil {
		return translatePersistenceError(err, identitycore.ErrUserNotFound, nil)
	}
	if n == 0 {
		return identitycore.ErrUserNotFound
	}
	return nil
}

// ApplyRedeemConcurrencyAdjustment atomically applies the delta and floors concurrency at zero.
func (r *ConcurrencyStore) ApplyRedeemConcurrencyAdjustment(ctx context.Context, id int64, delta int) error {
	const updateSQL = `
		UPDATE users
		SET concurrency = GREATEST(concurrency + $1, 0), updated_at = NOW()
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
		return identitycore.ErrUserNotFound
	}
	return nil
}
