package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

type RedeemParticipant struct {
	tx        *dbent.Tx
	mutations *RedeemMutations
}

// BalanceInTx binds balance writes to the caller's transaction. The caller owns commit, rollback, and post-commit
// notifications.
func BalanceInTx(tx *dbent.Tx) *BalanceStore {
	return NewBalanceStore(tx.Client())
}

// SubscriptionsInTx binds subscription reads, plan reads, and writes to one transaction connection.
func SubscriptionsInTx(tx *dbent.Tx, groups billing.SubscriptionGroupReader, clock billing.DateRuntime) *billing.SubscriptionService {
	return billing.NewSubscriptionService(groups, NewUserSubscriptionRepository(tx.Client()), SubscriptionMutationsInTx(tx), clock)
}

// RedeemInTx supplies balance and concurrency adjustments within the caller's transaction.
// The caller owns commit and rollback; RedeemService coordinates complete redemptions.
func RedeemInTx(tx *dbent.Tx, concurrency ...RedeemConcurrencyWriter) *RedeemParticipant {
	var writer RedeemConcurrencyWriter
	if len(concurrency) > 0 {
		writer = concurrency[0]
	}
	return &RedeemParticipant{tx: tx, mutations: NewRedeemMutations(tx.Client(), RedeemWriters{Balances: BalanceInTx(tx), Concurrency: writer})}
}

func (p *RedeemParticipant) ApplyBalance(ctx context.Context, id int64, amount float64) error {
	return p.mutations.ApplyBalance(dbent.NewTxContext(ctx, p.tx), id, amount)
}

func (p *RedeemParticipant) ApplyConcurrency(ctx context.Context, id int64, delta int) error {
	return p.mutations.ApplyConcurrency(dbent.NewTxContext(ctx, p.tx), id, delta)
}

// DeductRefundBalance returns the amount removed from the available nonnegative balance. The caller commits its
// transaction.
func (r *BalanceStore) DeductRefundBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	_, deducted, err := DeductAvailableBalance(ctx, clientFromContext(ctx, r.client), id, amount)
	return deducted, err
}

// CompensateRefundBalance restores a refund amount; positive amounts also increase total recharged.
func (r *BalanceStore) CompensateRefundBalance(ctx context.Context, id int64, amount float64) error {
	return r.UpdateBalance(ctx, id, amount)
}
