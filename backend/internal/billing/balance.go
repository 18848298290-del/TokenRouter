package billing

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Balance adjustments need the observed old value and the resulting or
// rejected requested balance. Returned changes let administrative flows
// describe successful writes and explain rejected amounts, while initial
// registration funds set an opening balance independently of cumulative
// recharge statistics.

// ErrBalanceNegative reports an adjustment that would produce a negative
// balance, allowing callers to distinguish rejected amounts from missing
// users or persistence errors.
var ErrBalanceNegative = apperror.BadRequest("BALANCE_NEGATIVE", "balance cannot be negative")

// BalanceChange holds the observed old balance and resulting or rejected requested amount.
type BalanceChange struct {
	Old float64 // observed prior USD balance
	// New is the resulting stored balance after success or the requested
	// result when ErrBalanceNegative rejects a change. Callers check the
	// accompanying error before interpreting this value as persisted.
	New float64
}

// BalanceAdjuster applies atomic balance updates and reports their success or rejection.
type BalanceAdjuster interface {
	// SetBalance replaces a user balance and reports its previous and new
	// values. Negative requests return ErrBalanceNegative; the PostgreSQL
	// adapter reads the existing value to include the rejected request
	// in BalanceChange.
	SetBalance(context.Context, int64, float64) (BalanceChange, error)
	// AdjustBalance adds a signed delta to the currently stored user
	// balance. A change below zero returns ErrBalanceNegative with the
	// observed and requested balances; successful updates report their
	// actual old and new values.
	AdjustBalance(context.Context, int64, float64) (BalanceChange, error)
}

// InitialUserFunds supplies the opening USD balance written during user creation.
type InitialUserFunds struct {
	Balance float64 // initial USD balance
}
