package postgres

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
)

// Concurrent registration grants recheck a code's status and capacity after
// acquiring that code's row lock. The lock serializes redemption checks on
// its global use count. One transaction keeps that recheck, the recipient's
// balance credit, a unique user receipt, and the global use-count increment
// consistent.
//
// The mutation binds a repository and billing writer to the newly opened Ent
// transaction. A validation callback reads the locked controls before any
// credit, and an existing receipt rejects repeat use by the same recipient.
// Commit occurs after every write succeeds; earlier failures attempt
// rollback. The service requests cache invalidation after this mutation
// returns success, and the caller supplies usable validation, clock, and
// balance callbacks.

// PromoBalance adds a registration grant through the caller-owned billing
// transaction.
type PromoBalance interface {
	// CreditRegistrationPromo adds the Promo amount to balance and positive
	// cumulative recharges.
	//
	// The writer uses the granting transaction, leaving commit, rollback, and
	// post-commit cache publication to its caller.
	CreditRegistrationPromo(context.Context, int64, float64) error
}
type (
	// PromoBalanceFactory binds a registration credit writer to the supplied Ent
	// transaction.
	PromoBalanceFactory func(*dbent.Tx) PromoBalance
	// PromoMutations coordinates locked registration grants, their receipts, and
	// total-use counting.
	PromoMutations struct {
		// client opens the transaction used by each ApplyCode call. The adapter
		// retains it and leaves the client's overall lifetime with its owner.
		client *dbent.Client // grant transaction creation client
		// balances receives the newly opened transaction and returns its
		// registration-credit writer. Callers supply a usable factory whose writer
		// participates in that transaction.
		// same-transaction billing writer factory
		balances PromoBalanceFactory
	}
)

// NewPromoMutations retains client and the same-transaction balance factory
// for registration grants.
//
// Both dependencies remain caller owned; ApplyCode requires a usable client
// and a factory returning a usable writer.
func NewPromoMutations(client *dbent.Client, balances PromoBalanceFactory) *PromoMutations {
	return &PromoMutations{client: client, balances: balances}
}

// ApplyCode credits one eligible code for userID and commits its receipt and
// usage count together.
//
// It opens a transaction on the bound client, row-locks the code, and calls
// validate before checking the recipient's existing receipt. The supplied now
// callback timestamps the receipt; validate and now must be callable.
// Existing usage returns
// ErrPromoCodeAlreadyUsed; callback, read, and write errors return before
// commit.
//
// The balance factory receives that same transaction, then credit, receipt
// insertion, and count increment execute before commit. Deferred rollback
// cleanup covers early exits and discards its own result; cache publication
// belongs to the successful caller.
func (m *PromoMutations) ApplyCode(ctx context.Context, userID int64, code string, validate func(*promotion.PromoCode) error, now func() time.Time) error {
	tx, err := m.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Early validation or write failure skips commit; attempt rollback on every
	// return and retain the original error if cleanup also fails.
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	repo := NewPromoCodeRepository(tx.Client())
	// Lock the code before validation and existing-receipt lookup so concurrent
	// grants serialize their checks of the shared use count.
	value, err := repo.GetByCodeForUpdate(txCtx, code)
	if err != nil {
		return err
	}
	if err = validate(value); err != nil {
		return err
	}
	existing, err := repo.GetUsageByPromoCodeAndUser(txCtx, value.ID, userID)
	if err != nil {
		return fmt.Errorf("check existing usage: %w", err)
	}
	if existing != nil {
		return promotion.ErrPromoCodeAlreadyUsed
	}
	// Credit billing, insert the recipient receipt, then increment total usage
	// on the same transaction; failure in any step prevents commit.
	if err = m.balances(tx).CreditRegistrationPromo(txCtx, userID, value.BonusAmount); err != nil {
		return fmt.Errorf("update user balance: %w", err)
	}
	usage := &promotion.PromoCodeUsage{PromoCodeID: value.ID, UserID: userID, BonusAmount: value.BonusAmount, UsedAt: now()}
	if err = repo.CreateUsage(txCtx, usage); err != nil {
		return fmt.Errorf("create usage record: %w", err)
	}
	if err = repo.IncrementUsedCount(txCtx, value.ID); err != nil {
		return fmt.Errorf("increment used count: %w", err)
	}
	// Publish the balance credit, recipient receipt, and code count together
	// after locked validation and every persistent grant write have succeeded.
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
