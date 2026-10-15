package postgres

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// checkout.go persists checkout drafts and recharge codes transactionally,
// then records external payment responses in separate updates.
// Checkout needs a committed local order before external payment initiation
// and an order-owned redeem credential for later balance delivery. Creation
// therefore saves the numeric order identity, writes its recharge code, and
// commits both before returning to the provider caller.
//
// Pending-count and paid-history checks run in that transaction but add no
// separate buyer lock, while daily history uses UTC payment confirmation time.
// Merchant-number allocation checks bounded fresh candidates without reserving
// one against concurrent inserts. Provider response persistence is a later
// independent update that retains empty details and zero expiration, and
// best-effort checkout failure writes FAILED without a prior-status guard.
// Those later writes can follow external progress, so their errors remain
// visible to recovery callers.

// CreateCheckout persists a pending order and its order-owned recharge code in
// one newly owned transaction. It checks pending/provider-processing count
// and, for positive daily limits, paid history since current UTC midnight
// before writing the supplied draft snapshots.
//
// Those limit reads acquire no separate buyer serialization lock, so this
// transaction alone does not reserve a per-user count or daily ceiling against
// every concurrent checkout. Merchant allocation returns an observed unused
// candidate before insertion. The order is saved first to obtain its numeric
// ID, then the recharge code is written using that identity before commit.
// Early failures run rollback cleanup; successful conversion is returned after
// commit, and provider initiation belongs to the caller afterward.
func (s *OrderStore) CreateCheckout(ctx context.Context, draft payment.CheckoutDraft) (*payment.Order, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	// Keep rollback cleanup for every pre-commit return while this store owns the
	// transaction. Successful commit completes the order/code writes before any
	// provider initiation by checkout.
	defer func() { _ = tx.Rollback() }()
	count, err := tx.PaymentOrder.Query().Where(paymentorder.UserIDEQ(draft.Order.UserID), paymentorder.StatusIn(payment.OrderStatusPending, payment.OrderStatusProcessing)).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("count pending orders: %w", err)
	}
	if err = payment.ValidateCheckoutPending(count, draft.MaxPending); err != nil {
		return nil, err
	}
	if draft.DailyLimit > 0 {
		// Daily purchase history starts at current UTC midnight, independently of
		// selector creation-time usage. Include confirmed paid/recharging/completed
		// rows before comparing the draft's current principal.
		now := time.Now().UTC()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		rows, e := tx.PaymentOrder.Query().Where(paymentorder.UserIDEQ(draft.Order.UserID), paymentorder.StatusIn(payment.OrderStatusPaid, payment.OrderStatusRecharging, payment.OrderStatusCompleted), paymentorder.PaidAtGTE(start)).All(ctx)
		if e != nil {
			return nil, fmt.Errorf("query daily usage: %w", e)
		}
		if err = payment.ValidateCheckoutDaily(orderValues(rows), draft.LimitAmount, draft.DailyLimit); err != nil {
			return nil, err
		}
	}
	// Interpret the draft timeout in minutes for local expiration.
	exp := time.Now().Add(time.Duration(draft.TimeoutMinutes) * time.Minute)
	outTradeNo, err := allocateCheckoutTradeNumber(ctx, tx)
	if err != nil {
		return nil, err
	}
	selectedInstanceID := payment.RefundStringValue(draft.Order.ProviderInstanceID)
	selectedProviderKey := payment.RefundStringValue(draft.Order.ProviderKey)
	providerSnapshot := draft.Order.ProviderSnapshot
	b := tx.PaymentOrder.Create().
		SetUserID(draft.Order.UserID).
		SetUserEmail(draft.Order.UserEmail).
		SetUserName(draft.Order.UserName).
		SetNillableUserNotes(draft.Order.UserNotes).
		SetAmount(draft.Order.Amount).
		SetPayAmount(draft.Order.PayAmount).
		SetFeeRate(draft.Order.FeeRate).
		SetFeeFixed(draft.Order.FeeFixed).
		SetFeeRateAmount(draft.Order.FeeRateAmount).
		SetFeeAmount(draft.Order.FeeAmount).
		SetRechargeCode("").
		SetOutTradeNo(outTradeNo).
		SetPaymentType(draft.Order.PaymentType).
		SetPaymentTradeNo("").
		SetOrderType(draft.Order.OrderType).
		SetStatus(payment.OrderStatusPending).
		SetExpiresAt(exp).
		SetClientIP(draft.Order.ClientIP).
		SetSrcHost(draft.Order.SrcHost)
	if draft.Order.SrcURL != nil {
		b.SetSrcURL(*draft.Order.SrcURL)
	}
	if selectedInstanceID != "" {
		b.SetProviderInstanceID(selectedInstanceID)
	}
	if selectedProviderKey != "" {
		b.SetProviderKey(selectedProviderKey)
	}
	if providerSnapshot != nil {
		b.SetProviderSnapshot(providerSnapshot)
	}
	if draft.Order.BillingSnapshot != nil {
		b.SetBillingSnapshot(draft.Order.BillingSnapshot)
	}
	if draft.Order.PlanID != nil {
		b.SetPlanID(*draft.Order.PlanID).SetPlanSnapshot(draft.Order.PlanSnapshot)
	}

	// Save the order first to obtain its numeric identity, then write the
	// correlated recharge code within the same transaction before publishing a
	// committed order.
	order, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}
	// Use the order ID with a bounded timestamp suffix.
	code := fmt.Sprintf("PAY-%d-%d", order.ID, time.Now().UnixNano()%100000)
	order, err = tx.PaymentOrder.UpdateOneID(order.ID).SetRechargeCode(code).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("set recharge code: %w", err)
	}
	// Commit after both order and recharge-code persistence have succeeded.
	// Return the stored observation only after this commit reports success to the
	// caller.
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit order transaction: %w", err)
	}
	return OrderFromEntity(order), nil
}

// allocateCheckoutTradeNumber returns a generated merchant candidate not
// observed in the supplied transaction's exact-number lookup, trying at most
// five candidates. A lookup failure returns immediately and exhaustion reports
// an allocation error.
//
// The lookup is a pre-insert observation rather than a reservation of that
// candidate. A concurrent insert can still conflict at later order
// persistence, whose unique constraint and error handling remain with
// CreateCheckout. This helper generates a fresh candidate after each observed
// collision and uses the payment generator's process-local date and ASCII
// suffix.
func allocateCheckoutTradeNumber(ctx context.Context, tx *dbent.Tx) (string, error) {
	// Bound repeated merchant candidate generation after observed lookup
	// collisions.
	const maxAttempts = 5
	// An observed collision triggers another generated candidate. The existence
	// read does not reserve the number, so later insert conflicts still return
	// through checkout persistence.
	for range maxAttempts {
		candidate := payment.GenerateOutTradeNo()
		exists, err := tx.PaymentOrder.Query().Where(paymentorder.OutTradeNo(candidate)).Exist(ctx)
		if err != nil {
			return "", fmt.Errorf("check out_trade_no uniqueness: %w", err)
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("generate unique out_trade_no: exhausted %d attempts", maxAttempts)
}

// PersistCheckoutResponse stores nonempty provider details and selected
// instance identity on an existing order. Supply a nonnil selection; nil
// provider response is an error, and zero provider expiration retains the
// draft's stored expiration. This write follows external initiation and
// applies no prior-status condition, so callers retain an error for local
// recovery if provider progress already exists.
func (s *OrderStore) PersistCheckoutResponse(ctx context.Context, orderID int64, sel *payment.InstanceSelection, pr *payment.CreatePaymentResponse) (*payment.Order, error) {
	if pr == nil {
		return nil, fmt.Errorf("payment provider returned an empty create response")
	}
	orderUpdate := s.client.PaymentOrder.UpdateOneID(orderID).
		SetNillablePaymentTradeNo(payment.NilIfEmpty(pr.TradeNo)).
		SetNillablePayURL(payment.NilIfEmpty(pr.PayURL)).
		SetNillableQrCode(payment.NilIfEmpty(pr.QRCode)).
		SetNillableProviderInstanceID(payment.NilIfEmpty(sel.InstanceID)).
		SetNillableProviderKey(payment.NilIfEmpty(sel.ProviderKey)).
		SetNillablePaymentCustomerID(payment.NilIfEmpty(pr.CustomerID)).
		SetNillablePaymentInvoiceID(payment.NilIfEmpty(pr.InvoiceID)).
		SetNillablePaymentInvoiceURL(payment.NilIfEmpty(pr.InvoiceURL)).
		SetNillablePaymentInvoicePdfURL(payment.NilIfEmpty(pr.InvoicePDF)).
		SetNillablePaymentInvoiceStatus(payment.NilIfEmpty(pr.InvoiceStatus))
	if !pr.ExpiresAt.IsZero() {
		orderUpdate = orderUpdate.SetExpiresAt(pr.ExpiresAt)
	}
	order, err := orderUpdate.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update order with payment details: %w", err)
	}
	return OrderFromEntity(order), nil
}

// FailCheckout writes FAILED on the addressed order without a prior-status
// condition. CreateOrder invokes it best-effort after initiation or response
// persistence failure; this operation retains other payment and timestamp
// columns according to Ent updates.
func (s *OrderStore) FailCheckout(ctx context.Context, id int64) error {
	_, err := s.client.PaymentOrder.UpdateOneID(id).SetStatus(payment.OrderStatusFailed).Save(ctx)
	return err
}

// CancelledCount counts ORDER_CANCELLED audits for an exact operator at or
// after since. It applies no upper time bound; cancellation limiting chooses
// its threshold and handles query failure before checkout creation.
func (s *OrderStore) CancelledCount(ctx context.Context, operator string, since time.Time) (int, error) {
	return s.client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("ORDER_CANCELLED"), paymentauditlog.OperatorEQ(operator), paymentauditlog.CreatedAtGTE(since)).Count(ctx)
}
