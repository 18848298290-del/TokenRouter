package payment

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// AuditLog records an order action, actor, and timestamp.
type AuditLog struct {
	ID        int64     `json:"id,omitempty"`
	OrderID   string    `json:"order_id,omitempty"`
	Action    string    `json:"action,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Operator  string    `json:"operator,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

func (s *OrderQueries) GetOrder(ctx context.Context, orderID, userID int64) (*Order, error) {
	o, err := s.store.Order(ctx, orderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != userID {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission for this order")
	}
	return o, nil
}

func (s *OrderQueries) GetOrderByID(ctx context.Context, orderID int64) (*Order, error) {
	o, err := s.store.Order(ctx, orderID)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	return o, nil
}

func (s *OrderQueries) GetUserOrders(ctx context.Context, userID int64, p OrderListParams) ([]*Order, int, error) {
	return s.store.GetUserOrders(ctx, userID, p)
}

func (s *OrderQueries) AdminListOrders(ctx context.Context, userID int64, p OrderListParams) ([]*Order, int, error) {
	return s.store.AdminListOrders(ctx, userID, p)
}

// GetOrderAuditLogs returns audit records for the order.
func (s *OrderQueries) GetOrderAuditLogs(ctx context.Context, id int64) ([]*AuditLog, error) {
	return s.store.OrderAuditLogs(ctx, id)
}
