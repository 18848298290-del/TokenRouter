package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	identitycore "github.com/TokenFlux/TokenRouter/internal/identity"
)

// concurrency.go persists direct and redemption-based changes to account
// concurrency allowances.
// Redemption can increase or decrease an account concurrency allowance. Direct
// adjustments add the supplied delta, while redeem-specific SQL floors the
// stored value at zero and filters soft-deleted accounts; both choose an Ent
// transaction from context when present.

// ConcurrencyStore persists account concurrency changes using the configured
// or context transaction client.
type ConcurrencyStore struct {
	client *dbent.Client // default account concurrency client
}

// NewConcurrencyStore retains the Ent client used for account concurrency
// persistence.
func NewConcurrencyStore(client *dbent.Client) *ConcurrencyStore {
	return &ConcurrencyStore{client: client}
}

// UpdateConcurrency adds amount to the supplied account allowance using the
// context transaction client when available. Missing affected accounts return
// ErrUserNotFound; this general adjustment does not apply the redemption zero
// floor.
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

// ApplyRedeemConcurrencyAdjustment applies a signed redeem delta and floors
// stored concurrency at zero in one SQL update. It filters deleted accounts,
// joins context transactions when present, and reports ErrUserNotFound for
// zero affected rows.
func (r *ConcurrencyStore) ApplyRedeemConcurrencyAdjustment(ctx context.Context, id int64, delta int) error {
	const updateSQL = `
		UPDATE users
		SET concurrency = GREATEST(concurrency + $1, 0), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`
	client := clientFromContext(ctx, r.client)
	// The SQL expression applies the delta and zero floor together, so concurrent
	// redeem adjustments use the stored allowance rather than a separately loaded
	// value.
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
