package postgres

import (
	"context"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Affiliate lifetime-cap checks need their cumulative read and next accrual
// to use the same locked profile. The callback receives a transaction
// context, and billing transfer factories bind balance changes to that
// transaction while its owner controls completion.

// TransferBalance credits affiliate quota through the caller-owned billing
// transaction.
type TransferBalance interface {
	// CreditAffiliateTransfer adds transferred quota to user balance and
	// positive cumulative recharges.
	//
	// The writer participates in the supplied transaction; its caller owns
	// completion and later cache publication.
	CreditAffiliateTransfer(context.Context, int64, float64) error
}

// TransferBalanceFactory binds a transfer balance writer to a supplied live
// Ent transaction.
type TransferBalanceFactory func(*dbent.Tx) TransferBalance

// clientFromContext selects a context-carried Ent transaction client before
// the supplied fallback.
//
// The returned client is borrowed; the transaction or fallback owner manages
// its lifetime and completion.
func clientFromContext(ctx context.Context, fallback *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return fallback
}

// WithLockedInviter invokes fn after confirming and locking the inviter's
// profile in withTx.
//
// The callback uses the provided transaction context, and the owning
// transaction retains the row lock until completion.
func (r *affiliateRepository) WithLockedInviter(ctx context.Context, id int64, fn func(context.Context) error) error {
	return r.withTx(ctx, func(txCtx context.Context, c *dbent.Client) error {
		// Acquire the inviter profile lock before the callback's cumulative read
		// and accrual, keeping that pair's cap decision on one transaction.
		rows, err := c.QueryContext(txCtx, "SELECT user_id FROM user_affiliates WHERE user_id=$1 FOR UPDATE", id)
		if err != nil {
			return fmt.Errorf("lock affiliate profile: %w", err)
		}
		exists := rows.Next()
		readErr := rows.Err()
		// Close the SELECT result before callback queries reuse the connection; the
		// profile lock remains owned by the enclosing transaction.
		closeErr := rows.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !exists {
			return identity.ErrUserNotFound
		}
		// Pass the same transaction context into callback work so nested repository
		// reads and grants participate while the inviter lock remains held.
		return fn(txCtx)
	})
}
