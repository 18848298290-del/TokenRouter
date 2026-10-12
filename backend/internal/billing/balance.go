package billing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

var ErrBalanceNegative = apperror.BadRequest("BALANCE_NEGATIVE", "balance cannot be negative")

// BalanceChange records the balance before and after an adjustment.
type BalanceChange struct {
	Old float64
	New float64
}

// BalanceAdjuster atomically changes a user's balance and returns the old and new values.
type BalanceAdjuster interface {
	SetBalance(context.Context, int64, float64) (BalanceChange, error)
	AdjustBalance(context.Context, int64, float64) (BalanceChange, error)
}

// InitialUserFunds is the registration balance grant; it does not increase total recharged.
type InitialUserFunds struct{ Balance float64 }
