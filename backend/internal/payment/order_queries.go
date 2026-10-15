package payment

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// order_queries.go exposes buyer-owned and administrative order reads, lists,
// and audit history.
// Order reads apply the supplied buyer identity at the user entry
// point, while administrator reads rely on caller authorization. Both
// single-order helpers map storage failures to not-found; paginated
// lists and audit reads delegate their filters, counts, and persistence
// errors to the configured store.

// AuditLog retains a stored payment action, details, actor, and creation timestamp.
type AuditLog struct {
	ID        int64     `json:"id,omitempty"`         // local audit record identifier
	OrderID   string    `json:"order_id,omitempty"`   // decimal local order ID
	Action    string    `json:"action,omitempty"`     // recorded payment action
	Detail    string    `json:"detail,omitempty"`     // recorded audit details
	Operator  string    `json:"operator,omitempty"`   // recorded action actor
	CreatedAt time.Time `json:"created_at,omitempty"` // audit creation timestamp
}

// GetOrder reads an order and checks that its recorded user matches
// userID. Every storage error becomes not-found, including read failures;
// a different recorded user returns forbidden, and a matching order
// is returned as supplied by storage.
func (s *OrderQueries) GetOrder(ctx context.Context, orderID, userID int64) (*Order, error) {
	o, err := s.store.Order(ctx, orderID)
	// Single-order callers receive the same not-found response for a
	// missing row and other read failures; this mapping hides the
	// underlying store error from their result.
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != userID {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission for this order")
	}
	return o, nil
}

// GetOrderByID reads an order without a user-ownership comparison.
// The caller establishes access to that ID; every storage error maps
// to not-found, and a successful value is returned as supplied by
// the configured store.
func (s *OrderQueries) GetOrderByID(ctx context.Context, orderID int64) (*Order, error) {
	o, err := s.store.Order(ctx, orderID)
	// Single-order callers receive the same not-found response for a
	// missing row and other read failures; this mapping hides the
	// underlying store error from their result.
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	return o, nil
}

// GetUserOrders delegates listing and the total matching count for
// the supplied user. The PostgreSQL store filters stored user ownership
// and selected status/type fields, then applies its pagination defaults;
// store errors are returned unchanged.
func (s *OrderQueries) GetUserOrders(ctx context.Context, userID int64, p OrderListParams) ([]*Order, int, error) {
	return s.store.GetUserOrders(ctx, userID, p)
}

// AdminListOrders delegates administrator listing and the matching
// count to storage. The caller establishes administrator access; the
// PostgreSQL store uses a positive user ID as an optional filter and
// applies keyword search and pagination rules.
func (s *OrderQueries) AdminListOrders(ctx context.Context, userID int64, p OrderListParams) ([]*Order, int, error) {
	return s.store.AdminListOrders(ctx, userID, p)
}

// GetOrderAuditLogs returns the order audit records from storage.
// Callers establish access to the requested order; the PostgreSQL
// store matches its decimal local ID and sorts records by creation
// time, returning query errors unchanged.
func (s *OrderQueries) GetOrderAuditLogs(ctx context.Context, id int64) ([]*AuditLog, error) {
	return s.store.OrderAuditLogs(ctx, id)
}
