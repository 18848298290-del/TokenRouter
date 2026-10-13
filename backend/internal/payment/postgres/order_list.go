package postgres

import (
	"context"
	"fmt"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// Payment list counts and pages use the same requested predicates but execute
// as separate queries, so concurrent order updates can change membership
// between them. User queries fix ownership, while administrator queries can
// retain all users or select a positive user ID and stored merchant/profile
// keyword.
//
// Page sizes and numbers use the shared normalization helper before offset
// calculation. Returned orders are sorted by creation time descending and
// converted through OrderFromEntity; equal timestamps have no added ID
// ordering here. Count or page errors abort that query result, while the bound
// client's own transaction context determines any stronger snapshot
// guarantees. The repository does not apply HTTP authentication to an
// administrator list call.

// GetUserOrders returns the supplied user's matching order count and
// creation-descending page. Nonempty status, order type, and method filters
// compare their supplied strings directly; pagination uses QueryPagination
// defaults and cap.
//
// The count and page are separate queries without a shared snapshot
// established here, so concurrent updates can change page membership after
// counting. Count failure returns before the page query, and page failure
// returns an error instead of a partial list. Converted orders use
// OrderFromEntity ownership rules, and equal creation times have no additional
// sort key.
func (s *OrderStore) GetUserOrders(ctx context.Context, userID int64, p payment.OrderListParams) ([]*payment.Order, int, error) {
	q := s.client.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID))
	if p.Status != "" {
		q = q.Where(paymentorder.StatusEQ(p.Status))
	}
	if p.OrderType != "" {
		q = q.Where(paymentorder.OrderTypeEQ(p.OrderType))
	}
	if p.PaymentType != "" {
		q = q.Where(paymentorder.PaymentTypeEQ(p.PaymentType))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count user orders: %w", err)
	}
	ps, pg := payment.QueryPagination(p.PageSize, p.Page)
	orders, err := q.Order(dbent.Desc(paymentorder.FieldCreatedAt)).Limit(ps).Offset((pg - 1) * ps).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query user orders: %w", err)
	}
	return orderValues(orders), total, nil
}

// AdminListOrders returns the administrator-selected count and
// creation-descending page, narrowing to a user only when userID is positive.
// Nonempty status/type/method fields and merchant/email/name keyword
// predicates apply before counting.
//
// Count and page reads remain separate observations, with normalized page size
// and offset used for the latter. Concurrent writes can alter the selected
// page after count, and equal creation timestamps have no additional tie key.
// This method does not establish administrator authentication; its HTTP caller
// applies access checks before invoking the query.
func (s *OrderStore) AdminListOrders(ctx context.Context, userID int64, p payment.OrderListParams) ([]*payment.Order, int, error) {
	q := s.client.PaymentOrder.Query()
	if userID > 0 {
		q = q.Where(paymentorder.UserIDEQ(userID))
	}
	if p.Status != "" {
		q = q.Where(paymentorder.StatusEQ(p.Status))
	}
	if p.OrderType != "" {
		q = q.Where(paymentorder.OrderTypeEQ(p.OrderType))
	}
	if p.PaymentType != "" {
		q = q.Where(paymentorder.PaymentTypeEQ(p.PaymentType))
	}
	if p.Keyword != "" {
		q = q.Where(paymentorder.Or(
			paymentorder.OutTradeNoContainsFold(p.Keyword),
			paymentorder.UserEmailContainsFold(p.Keyword),
			paymentorder.UserNameContainsFold(p.Keyword),
		))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count admin orders: %w", err)
	}
	ps, pg := payment.QueryPagination(p.PageSize, p.Page)
	orders, err := q.Order(dbent.Desc(paymentorder.FieldCreatedAt)).Limit(ps).Offset((pg - 1) * ps).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query admin orders: %w", err)
	}
	return orderValues(orders), total, nil
}
