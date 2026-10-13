package postgres

import (
	"context"
	"strconv"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/ent/subscriptionplan"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// Past payment eligibility depends on confirmed payment time and positive
// gateway money, so a refunded or unfinished entitlement order can still
// qualify. That eligibility read can join an Ent transaction carried by
// redemption context, while other order queries use the store's bound client.
//
// Dashboard inputs select their own statuses and half-open paid interval, and
// current plan names remain separate reads from recorded sale snapshots. Query
// results convert persisted fields through the qualified Order.Clone rules.
// Order rebate execution uses an injected transaction participant and
// errorless ordinary audit callback; its mandatory claim/final records remain
// with the rebate transaction. Construction retains the client and first
// runtime option without starting any background work.

// OrderRebates applies the order-linked promotion grant through supplied
// transaction context.
type OrderRebates interface {
	// AccrueInviteRebateForOrder computes and persists promotion quota for the
	// user and optional source order. ApplyOrderRebate supplies its Ent
	// transaction context; this operation returns the granted amount for the
	// final audit state.
	AccrueInviteRebateForOrder(context.Context, int64, float64, *int64) (float64, error)
}

// OrderStoreRuntime supplies rebate participation and ordinary payment audit
// observation.
type OrderStoreRuntime struct {
	// Rebates supplies participation for the transaction owned by
	// ApplyOrderRebate. Its returned operation uses the supplied transaction
	// context, and that outer order store owns completion.
	Rebates func(*dbent.Tx) OrderRebates
	// Audit records ordinary failure observations through an errorless callback.
	// Mandatory order rebate claims and final records are written separately
	// inside their owned transaction.
	Audit func(context.Context, int64, string, string, map[string]any)
}

// OrderStore reads and persists payment orders using its bound Ent client.
type OrderStore struct {
	// client is the bound order persistence connection retained from
	// NewOrderStore. Its owner keeps it usable; HasPaidOrder can prefer a
	// transaction supplied in the operation context.
	client  *dbent.Client
	runtime OrderStoreRuntime // rebate and audit operations
}

// NewOrderStore binds the caller-owned Ent client and the first optional
// runtime value. Missing Audit becomes a no-op callback; rebate-enabled
// execution still needs a usable Rebates factory for its transaction.
func NewOrderStore(client *dbent.Client, options ...OrderStoreRuntime) *OrderStore {
	var runtime OrderStoreRuntime
	if len(options) > 0 {
		runtime = options[0]
	}
	if runtime.Audit == nil {
		runtime.Audit = func(context.Context, int64, string, string, map[string]any) {}
	}
	return &OrderStore{client: client, runtime: runtime}
}

// Order reads the addressed local order and converts its persisted
// observation.
func (s *OrderStore) Order(ctx context.Context, id int64) (*payment.Order, error) {
	o, e := s.client.PaymentOrder.Get(ctx, id)
	return OrderFromEntity(o), e
}

// orderValues preserves a nil input slice or returns a new slice converted
// through OrderFromEntity. Each element follows Order.Clone detachment rules,
// including nil entity elements and unsupported mutable snapshot references.
func orderValues(rows []*dbent.PaymentOrder) []*payment.Order {
	if rows == nil {
		return nil
	}
	out := make([]*payment.Order, len(rows))
	for i, v := range rows {
		out[i] = OrderFromEntity(v)
	}
	return out
}

// PaidOrders returns the supplied status set with paid_at in the half-open
// start/end interval. It applies no implicit status choice or sort order;
// dashboard callers select their paid, recharging, and completed states.
func (s *OrderStore) PaidOrders(ctx context.Context, start, end time.Time, statuses []string) ([]*payment.Order, error) {
	rows, e := s.client.PaymentOrder.Query().Where(paymentorder.StatusIn(statuses...), paymentorder.PaidAtGTE(start), paymentorder.PaidAtLT(end)).All(ctx)
	return orderValues(rows), e
}

// HasPaidOrder reports a user order with nonnil paid_at and positive
// PayAmount, independent of its current lifecycle state. It selects an Ent
// transaction carried by context when present, allowing redemption eligibility
// to share that connection; refunded orders can still qualify as past payment.
func (s *OrderStore) HasPaidOrder(ctx context.Context, userID int64) (bool, error) {
	client := s.client
	if tx := dbent.TxFromContext(ctx); tx != nil {
		client = tx.Client()
	}
	return client.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(userID),
		paymentorder.PaidAtNotNil(),
		paymentorder.PayAmountGT(0),
	).Exist(ctx)
}

// PendingCount returns the total pending and provider-processing order count
// from storage.
func (s *OrderStore) PendingCount(ctx context.Context) (int, error) {
	return s.client.PaymentOrder.Query().Where(paymentorder.StatusIn(payment.OrderStatusPending, payment.OrderStatusProcessing)).Count(ctx)
}

// PlanNames returns current persisted names for supplied local plan IDs in a
// new map. Missing IDs contribute no entry, and query failure returns before
// labels are published to dashboard consumers.
func (s *OrderStore) PlanNames(ctx context.Context, ids []int64) (map[int64]string, error) {
	rows, e := s.client.SubscriptionPlan.Query().Where(subscriptionplan.IDIn(ids...)).All(ctx)
	if e != nil {
		return nil, e
	}
	names := make(map[int64]string, len(rows))
	for _, v := range rows {
		names[v.ID] = v.Name
	}
	return names, nil
}

// OrderAuditLogs returns the order's text-keyed audit rows in creation-time
// order. It builds new domain values after a successful read; timestamp ties
// have no additional ID ordering here, and detail strings remain as stored.
func (s *OrderStore) OrderAuditLogs(ctx context.Context, id int64) ([]*payment.AuditLog, error) {
	rows, err := s.client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(id, 10))).Order(paymentauditlog.ByCreatedAt()).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*payment.AuditLog, len(rows))
	for i, v := range rows {
		out[i] = &payment.AuditLog{ID: v.ID, OrderID: v.OrderID, Action: v.Action, Detail: v.Detail, Operator: v.Operator, CreatedAt: v.CreatedAt}
	}
	return out, nil
}
