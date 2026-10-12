package postgres

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/apikey"
	dbuser "github.com/TokenFlux/TokenRouter/ent/user"
	"github.com/TokenFlux/TokenRouter/ent/usersubscription"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// SubscriptionMutations manages entitlement transactions, user-before-subscription lock ordering, and Key rebinding.
type SubscriptionMutations struct {
	client *dbent.Client
	bound  *dbent.Tx
}

func NewSubscriptionMutations(client *dbent.Client) *SubscriptionMutations {
	return &SubscriptionMutations{client: client}
}

// SubscriptionMutationsInTx binds the caller's transaction; the caller retains commit and rollback ownership.
func SubscriptionMutationsInTx(tx *dbent.Tx) *SubscriptionMutations {
	return &SubscriptionMutations{bound: tx, client: tx.Client()}
}

func (m *SubscriptionMutations) transaction(ctx context.Context) *dbent.Tx {
	if m.bound != nil {
		return m.bound
	}
	return dbent.TxFromContext(ctx)
}

func (m *SubscriptionMutations) HasPersistence(ctx context.Context) bool {
	return m.transaction(ctx) != nil || m.client != nil
}

func (m *SubscriptionMutations) Within(ctx context.Context, fn func(context.Context) error) error {
	if tx := m.transaction(ctx); tx != nil {
		return fn(dbent.NewTxContext(ctx, tx))
	}
	if m.client == nil {
		return fn(ctx)
	}
	tx, err := m.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(dbent.NewTxContext(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (m *SubscriptionMutations) GetPlan(ctx context.Context, id int64) (*billing.SubscriptionPlan, error) {
	client := m.client
	if tx := m.transaction(ctx); tx != nil {
		client = tx.Client()
	}
	if client == nil {
		return nil, fmt.Errorf("ent client is nil")
	}
	plan, err := client.SubscriptionPlan.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return PlanFromEntity(plan), nil
}

func (m *SubscriptionMutations) LockGrantUser(ctx context.Context, id int64) error {
	tx := m.transaction(ctx)
	if tx == nil {
		return nil
	}
	if _, err := tx.User.Query().Where(dbuser.IDEQ(id)).ForUpdate().Only(ctx); err != nil {
		return fmt.Errorf("lock user %d: %w", id, err)
	}
	return nil
}

func (m *SubscriptionMutations) LockSelfRevoke(ctx context.Context, userID, subscriptionID int64) error {
	tx := m.transaction(ctx)
	if tx == nil {
		return nil
	}
	if err := m.LockGrantUser(ctx, userID); err != nil {
		return err
	}
	if _, err := tx.UserSubscription.Query().Where(usersubscription.IDEQ(subscriptionID)).ForUpdate().Only(ctx); err != nil {
		return billing.ErrSubscriptionNotFound
	}
	return nil
}

func (m *SubscriptionMutations) RebindKeys(ctx context.Context, oldID, newID int64) (int, error) {
	if m.client == nil || oldID <= 0 || newID <= 0 {
		return 0, nil
	}
	client := m.client
	if tx := m.transaction(ctx); tx != nil {
		client = tx.Client()
	}
	return client.APIKey.Update().Where(apikey.BillingModeEQ(billing.APIKeyBillingModeSubscription), apikey.PreferredSubscriptionIDEQ(oldID), apikey.DeletedAtIsNil()).SetPreferredSubscriptionID(newID).SetUpdatedAt(time.Now()).Save(ctx)
}

// LockSubscription locks the user before the subscription, matching grant, restore, and settlement ordering.
// The initial read locates the user lock; callers reread full subscription state after acquiring locks.
func (m *SubscriptionMutations) LockSubscription(ctx context.Context, id int64) error {
	tx := m.transaction(ctx)
	if tx == nil {
		return nil
	}
	sub, err := tx.Client().UserSubscription.Query().Where(usersubscription.IDEQ(id)).Select(usersubscription.FieldUserID).Only(ctx)
	if err != nil {
		return translatePersistenceError(err, billing.ErrSubscriptionNotFound, nil)
	}
	if err := m.LockGrantUser(ctx, sub.UserID); err != nil {
		return err
	}
	_, err = tx.UserSubscription.Query().Where(usersubscription.IDEQ(id)).ForUpdate().Only(ctx)
	return translatePersistenceError(err, billing.ErrSubscriptionNotFound, nil)
}
