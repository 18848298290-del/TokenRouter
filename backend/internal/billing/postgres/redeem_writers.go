package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// RedeemBalanceWriter applies balance changes; identity-owned concurrency changes use RedeemConcurrencyWriter.
type RedeemBalanceWriter interface {
	UpdateBalance(context.Context, int64, float64) error
	ApplyRedeemBalanceAdjustment(context.Context, int64, float64) error
}
type RedeemConcurrencyWriter interface {
	UpdateConcurrency(context.Context, int64, int) error
	ApplyRedeemConcurrencyAdjustment(context.Context, int64, int) error
}

// RedeemWriters forwards entitlement changes with the caller's transaction context.
type RedeemWriters struct {
	Balances    RedeemBalanceWriter
	Concurrency RedeemConcurrencyWriter
}

func (w RedeemWriters) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	return w.Balances.UpdateBalance(ctx, id, amount)
}

func (w RedeemWriters) ApplyRedeemBalanceAdjustment(ctx context.Context, id int64, amount float64) error {
	return w.Balances.ApplyRedeemBalanceAdjustment(ctx, id, amount)
}

func (w RedeemWriters) UpdateConcurrency(ctx context.Context, id int64, delta int) error {
	if w.Concurrency == nil {
		return billing.ErrRedeemConcurrencyUnsupported
	}
	return w.Concurrency.UpdateConcurrency(ctx, id, delta)
}

func (w RedeemWriters) ApplyRedeemConcurrencyAdjustment(ctx context.Context, id int64, delta int) error {
	if w.Concurrency == nil {
		return billing.ErrRedeemConcurrencyUnsupported
	}
	return w.Concurrency.ApplyRedeemConcurrencyAdjustment(ctx, id, delta)
}
