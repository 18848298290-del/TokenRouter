package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Redemption funds belong to billing, while user concurrency belongs to
// identity. The combined writer forwards the same operation context to both
// owners so their adapters can share the Ent transaction chosen by the
// redemption service; absent concurrency support returns a domain error before
// its update.

// RedeemBalanceWriter supplies balance increments and zero-floored adjustments
// on the passed context.
type RedeemBalanceWriter interface {
	// UpdateBalance adds the supplied amount using the operation's transaction
	// context. The concrete billing writer determines recharge accumulation,
	// missing-user handling and persistence errors; the outer caller owns commit
	// or rollback.
	UpdateBalance(context.Context, int64, float64) error
	// ApplyRedeemBalanceAdjustment atomically adds delta and floors stored
	// balance at zero. The concrete billing writer uses the supplied transaction
	// context and returns persistence errors; callers own transaction completion.
	ApplyRedeemBalanceAdjustment(context.Context, int64, float64) error
}

// RedeemConcurrencyWriter supplies identity-owned concurrency increments and
// zero-floored adjustments on the passed context.
type RedeemConcurrencyWriter interface {
	// UpdateConcurrency adds the supplied concurrency delta using the operation
	// context. The identity owner applies its update and missing-user rules,
	// while the outer caller owns commit or rollback.
	UpdateConcurrency(context.Context, int64, int) error
	// ApplyRedeemConcurrencyAdjustment atomically adds delta and floors stored
	// concurrency at zero. The concrete identity writer uses the supplied
	// transaction context and reports persistence errors; callers own transaction
	// completion.
	ApplyRedeemConcurrencyAdjustment(context.Context, int64, int) error
}

// RedeemWriters combines required balance writes with optional identity
// concurrency writes for redemption.
type RedeemWriters struct {
	// Balances is required for balance operations. This wrapper calls it directly
	// with the supplied context and leaves persistence details with that owner.
	Balances RedeemBalanceWriter // required billing balance writer
	// Concurrency is optional when identity adjustments are unavailable. Both
	// concurrency methods return ErrRedeemConcurrencyUnsupported before any
	// operation when this field is nil.
	Concurrency RedeemConcurrencyWriter // optional identity concurrency writer
}

// UpdateBalance forwards amount to the required balance writer using the
// supplied transaction context. The concrete balance owner applies its
// cumulative recharge and missing-user rules; this wrapper does not start or
// finish a transaction.
func (w RedeemWriters) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	return w.Balances.UpdateBalance(ctx, id, amount)
}

// ApplyRedeemBalanceAdjustment forwards delta to the required balance floor
// writer on the supplied context. The concrete owner clamps funds and reports
// persistence failures, while the outer caller owns transaction completion.
func (w RedeemWriters) ApplyRedeemBalanceAdjustment(ctx context.Context, id int64, amount float64) error {
	return w.Balances.ApplyRedeemBalanceAdjustment(ctx, id, amount)
}

// UpdateConcurrency forwards delta to the identity writer when configured.
// Missing Concurrency returns ErrRedeemConcurrencyUnsupported before writing;
// the same supplied context carries transaction participation to the owner,
// and this wrapper creates no transaction.
func (w RedeemWriters) UpdateConcurrency(ctx context.Context, id int64, delta int) error {
	if w.Concurrency == nil {
		return billing.ErrRedeemConcurrencyUnsupported
	}
	return w.Concurrency.UpdateConcurrency(ctx, id, delta)
}

// ApplyRedeemConcurrencyAdjustment forwards delta to the identity floor writer
// when configured. Missing Concurrency returns
// ErrRedeemConcurrencyUnsupported; the concrete owner clamps entitlement and
// reports persistence errors using the supplied transaction context.
func (w RedeemWriters) ApplyRedeemConcurrencyAdjustment(ctx context.Context, id int64, delta int) error {
	if w.Concurrency == nil {
		return billing.ErrRedeemConcurrencyUnsupported
	}
	return w.Concurrency.ApplyRedeemConcurrencyAdjustment(ctx, id, delta)
}
