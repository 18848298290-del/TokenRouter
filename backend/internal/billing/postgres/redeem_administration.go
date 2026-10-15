package postgres

import (
	"context"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
)

// redeem_administration.go runs redemption administration callbacks in joined
// or owned transactions.
// Code administration can join a caller's Ent transaction, while
// adjustment-history recording starts its own transaction through the
// configured client. Callback writes use the selected transaction context so
// related credential and history changes can complete together.
//
// An owned transaction commits after the callback succeeds and schedules
// rollback for failures; its cleanup error remains secondary to the returned
// outcome. Joined administration performs no inner commit or savepoint, so
// the outer owner decides whether to abort its earlier and callback writes.
// With a nil configured client, direct callbacks rely on their own persistence
// scope, while plan lookup still needs a usable selected connection.

// RedeemAdministrationMutations runs administrative code and adjustment
// callbacks with distinct transaction ownership rules.
type RedeemAdministrationMutations struct {
	// client supplies owned transactions when configured. Mutation can instead
	// reuse a context transaction, while Adjustment always starts through this
	// configured client when present.
	client *dbent.Client // optional default administration client
}

// NewRedeemAdministrationMutations binds an optional default Ent client for
// code administration. A nil client runs mutation callbacks directly, while
// plan reads then need a usable context transaction; the caller owns the
// client lifecycle.
func NewRedeemAdministrationMutations(client *dbent.Client) *RedeemAdministrationMutations {
	return &RedeemAdministrationMutations{client: client}
}

// Mutation invokes fn with the credential administration transaction selected
// for this call. A transaction already carried in ctx is reused, and a nil
// configured client permits direct callback execution when none is present.
//
// Otherwise it starts owned work, passes its transaction in callback context,
// and commits after the callback succeeds. Deferred rollback attempts cleanup
// on failure and discards its error. Joined work has no inner commit or
// savepoint, so an error leaves recovery of the outer transaction to its owner.
func (m *RedeemAdministrationMutations) Mutation(ctx context.Context, fn func(context.Context) error) error {
	if dbent.TxFromContext(ctx) != nil || m.client == nil {
		return fn(ctx)
	}
	tx, err := m.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin redeem code mutation transaction: %w", err)
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
		return fmt.Errorf("commit redeem code mutation transaction: %w", err)
	}
	return nil
}

// Adjustment invokes fn in an adjustment-history transaction opened through
// the configured client, or directly when that client is nil. An incoming
// context transaction does not select this owned transaction; its callback
// receives the newly created transaction context.
//
// Successful callback writes precede commit, keeping their credential history
// in the same owned unit of work. Callback or commit failure returns while
// deferred rollback attempts cleanup and discards its own error. Direct
// nil-client callbacks rely on the caller's chosen persistence and recovery.
func (m *RedeemAdministrationMutations) Adjustment(ctx context.Context, fn func(context.Context) error) error {
	if m.client == nil {
		return fn(ctx)
	}
	tx, err := m.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin adjustment redeem transaction: %w", err)
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
		return fmt.Errorf("commit adjustment redeem transaction: %w", err)
	}
	return nil
}

// PlanExists reads the requested plan through the context transaction or
// default client. The caller supplies a usable selected connection; missing
// plans and other persistence errors are returned directly for administration
// validation.
func (m *RedeemAdministrationMutations) PlanExists(ctx context.Context, id int64) error {
	_, err := clientFromContext(ctx, m.client).SubscriptionPlan.Get(ctx, id)
	return err
}
