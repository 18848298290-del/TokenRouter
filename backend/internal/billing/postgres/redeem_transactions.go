package postgres

import (
	"context"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Redemption applies balance or concurrency through an injected writer on the
// transaction context supplied to each operation. Nonnegative values use the
// ordinary increment methods; negative values require the paired floor-writer
// capability so their resulting entitlement can be clamped by its owner.
//
// Within starts through the configured client and provides that transaction to
// the callback before committing. It does not reuse an outer context
// transaction automatically. The callback coordinates code validation,
// entitlement writes and usage records, while these adapters leave cache
// invalidation and rebate work with the service after transaction completion.

// RedeemUserWriter supplies transaction-aware ordinary balance and concurrency
// increments for redemption.
type RedeemUserWriter interface {
	// UpdateBalance adds the supplied amount using the operation's transaction
	// context. The concrete billing writer determines recharge accumulation,
	// missing-user handling and persistence errors; the outer caller owns commit
	// or rollback.
	UpdateBalance(context.Context, int64, float64) error
	// UpdateConcurrency adds the supplied concurrency delta using the operation
	// context. The identity owner applies its update and missing-user rules,
	// while the outer caller owns commit or rollback.
	UpdateConcurrency(context.Context, int64, int) error
}

// redeemFloorWriter supplies the paired floor operations for negative
// entitlement changes. Both ApplyRedeemBalanceAdjustment and
// ApplyRedeemConcurrencyAdjustment must be implemented before RedeemMutations
// can select either negative path on the ordinary writer.
type redeemFloorWriter interface {
	// ApplyRedeemBalanceAdjustment atomically adds delta and floors stored
	// balance at zero. The concrete billing writer uses the supplied transaction
	// context and returns persistence errors; callers own transaction completion.
	ApplyRedeemBalanceAdjustment(context.Context, int64, float64) error
	// ApplyRedeemConcurrencyAdjustment atomically adds delta and floors stored
	// concurrency at zero. The concrete identity writer uses the supplied
	// transaction context and reports persistence errors; callers own transaction
	// completion.
	ApplyRedeemConcurrencyAdjustment(context.Context, int64, int) error
}

// RedeemMutations binds Ent transaction execution and the injected entitlement
// update writer.
type RedeemMutations struct {
	// client starts the owned transaction in Within. The application keeps this
	// connection usable until redemption callbacks and their completion have ended.
	client *dbent.Client // bound redemption transaction client
	// users receives the transaction context selected by the caller or Within.
	// Negative adjustments require its paired floor capability, and the caller
	// keeps this writer usable.
	users RedeemUserWriter // entitlement increment and floor writer
}

// NewRedeemMutations binds a client and entitlement writer without opening a
// transaction. Within requires the client to start one; Apply methods use the
// passed operation context, and negative values require both floor-writer
// methods.
func NewRedeemMutations(client *dbent.Client, users RedeemUserWriter) *RedeemMutations {
	return &RedeemMutations{client: client, users: users}
}

// Within invokes fn in a newly owned transaction through the bound client.
// It installs that transaction in the callback context, where entitlement and
// redemption repositories select the same connection for their writes.
//
// Callback success precedes commit so a grant and its usage records complete
// together. Callback or commit failure returns with deferred rollback cleanup
// attempted; its error is discarded. An outer context transaction is not joined,
// and the service performs cache and rebate effects after this call completes.
func (m *RedeemMutations) Within(ctx context.Context, fn func(context.Context) error) error {
	tx, err := m.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Rollback remains scheduled until the owned transaction returns, releasing
	// callback work on failure; a successful commit makes that later rollback
	// attempt harmless.
	defer func() { _ = tx.Rollback() }()
	if err := fn(dbent.NewTxContext(ctx, tx)); err != nil {
		return err
	}
	// Commit after the callback's writes have succeeded, so a callback error
	// leaves the owned transaction available to its deferred rollback.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ApplyBalance forwards a nonnegative amount to the ordinary balance increment
// writer. Negative amounts require the writer to implement both
// redeemFloorWriter methods, then call its floor adjustment; absent capability
// returns ErrRedeemBalanceUnsupported before any write.
func (m *RedeemMutations) ApplyBalance(ctx context.Context, id int64, amount float64) error {
	// Negative balance changes need the paired floor-writer capability; checking
	// that capability before dispatch prevents an unsupported adjustment from writing.
	if amount < 0 {
		writer, ok := m.users.(redeemFloorWriter)
		if !ok {
			return billing.ErrRedeemBalanceUnsupported
		}
		return writer.ApplyRedeemBalanceAdjustment(ctx, id, amount)
	}
	return m.users.UpdateBalance(ctx, id, amount)
}

// ApplyConcurrency forwards a nonnegative delta to the ordinary concurrency
// writer. Negative values require both redeemFloorWriter methods before
// invoking the concurrency floor; absent capability returns
// ErrRedeemConcurrencyUnsupported, and the supplied context controls
// transaction participation.
func (m *RedeemMutations) ApplyConcurrency(ctx context.Context, id int64, delta int) error {
	// Negative concurrency changes need the paired floor-writer capability before
	// dispatch, so an ordinary increment writer cannot silently apply an unfloored decrease.
	if delta < 0 {
		writer, ok := m.users.(redeemFloorWriter)
		if !ok {
			return billing.ErrRedeemConcurrencyUnsupported
		}
		return writer.ApplyRedeemConcurrencyAdjustment(ctx, id, delta)
	}
	return m.users.UpdateConcurrency(ctx, id, delta)
}
