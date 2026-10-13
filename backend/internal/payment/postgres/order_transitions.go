package postgres

import (
	"context"
	"strconv"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// Payment observations and fulfillment owners can update the same order, so
// transitions compare accepted current status and optional timestamp version
// before staging changes. Failure clearing and supplied detail writes occur in
// the same conditional update, and Ent mutation time can invalidate an earlier
// completion observation.
//
// Fulfillment admission records RECHARGING and a claim time for paid/failed or
// stale recharging rows, returning an affected count before grant work begins.
// The core rereads that time for later conditional completion rather than
// retaining a row lock during entitlement delivery. Ordinary audit presence is
// a weaker observation that suppresses query errors into false; financial
// recovery claims use their transaction-specific persistence methods.

// IsNotFound reports whether Ent classified an order lookup as missing.
func (s *OrderStore) IsNotFound(err error) bool { return dbent.IsNotFound(err) }

// OrderByTradeNumber reads one exact stored merchant number and converts the
// order. Notification processing owns any later legacy numeric lookup, so this
// storage query retains exact number identity.
func (s *OrderStore) OrderByTradeNumber(ctx context.Context, no string) (*payment.Order, error) {
	o, e := s.client.PaymentOrder.Query().Where(paymentorder.OutTradeNo(no)).Only(ctx)
	return OrderFromEntity(o), e
}

// TransitionOrder returns the affected count for the addressed row in an
// accepted From state, optionally matching its UpdatedAt version. It stages
// target status and supplied pointer/nonempty details in one Ent update.
//
// ClearFailure clears old failure time and text before optional replacement
// failure fields are added. Empty trade/invoice strings retain their existing
// columns, while nonnil scalar/time pointers stage their values. Ent supplies
// update time when it was not provided, making later timestamp-conditioned
// claims sensitive to intervening writes. Zero count leaves callers to reread
// competing state; this method does not decide which observed outcome wins.
func (s *OrderStore) TransitionOrder(ctx context.Context, c payment.OrderTransition) (int, error) {
	// Keep status, optional version, and detail changes in one conditional
	// update. A zero match reports lost expected state before any caller chooses
	// recovery from a reread.
	u := s.client.PaymentOrder.Update().Where(paymentorder.IDEQ(c.ID), paymentorder.StatusIn(c.From...)).SetStatus(c.Status)
	if c.Version != nil {
		u.Where(paymentorder.UpdatedAtEQ(*c.Version))
	}
	// Clear prior failure fields before staging replacement pointers. The same
	// transition can reset old diagnostics and still write its chosen new failure
	// time or reason.
	if c.ClearFailure {
		u.ClearFailedAt().ClearFailedReason()
	}
	if c.TradeNo != "" {
		u.SetPaymentTradeNo(c.TradeNo)
	}
	if c.PayAmount != nil {
		u.SetPayAmount(*c.PayAmount)
	}
	if c.PaidAt != nil {
		u.SetPaidAt(*c.PaidAt)
	}
	if c.CompletedAt != nil {
		u.SetCompletedAt(*c.CompletedAt)
	}
	if c.FailedAt != nil {
		u.SetFailedAt(*c.FailedAt)
	}
	if c.FailedReason != nil {
		u.SetFailedReason(*c.FailedReason)
	}
	if c.InvoiceID != "" {
		u.SetPaymentInvoiceID(c.InvoiceID)
	}
	if c.InvoiceURL != "" {
		u.SetPaymentInvoiceURL(c.InvoiceURL)
	}
	if c.InvoicePDF != "" {
		u.SetPaymentInvoicePdfURL(c.InvoicePDF)
	}
	if c.InvoiceStatus != "" {
		u.SetPaymentInvoiceStatus(c.InvoiceStatus)
	}
	return u.Save(ctx)
}

// ClaimFulfillment conditionally writes RECHARGING, the supplied claim time,
// and cleared failure fields for paid, failed, or sufficiently stale
// recharging orders. The stale comparison is inclusive and occurs in that same
// update.
//
// The returned affected count reports admission rather than a retained
// database lock over grant work. AcquirePaymentFulfillmentLease rereads the
// stored timestamp for its later completion/failure version check; entitlement
// replay protection belongs to the grant operation. The claim applies its
// supplied times directly and performs no lease renewal or paid_at requirement
// of its own.
func (s *OrderStore) ClaimFulfillment(ctx context.Context, id int64, now, stale time.Time) (int, error) {
	return s.client.PaymentOrder.Update().Where(paymentorder.IDEQ(id), paymentorder.Or(paymentorder.StatusIn(payment.OrderStatusPaid, payment.OrderStatusFailed), paymentorder.And(paymentorder.StatusEQ(payment.OrderStatusRecharging), paymentorder.UpdatedAtLTE(stale)))).SetStatus(payment.OrderStatusRecharging).SetUpdatedAt(now).ClearFailedAt().ClearFailedReason().Save(ctx)
}

// HasAudit reports whether an order/action count is positive. It discards the
// Ent count error, so false can represent missing history or query failure and
// must not be treated as a successful transaction claim.
func (s *OrderStore) HasAudit(ctx context.Context, id int64, action string) bool {
	// Discard the count error and test its value directly. A zero result from
	// absence or failed reading is indistinguishable to callers, while financial
	// claims use separate transactions.
	count, _ := s.client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(id, 10)), paymentauditlog.ActionEQ(action)).Limit(1).Count(ctx)
	return count > 0
}
