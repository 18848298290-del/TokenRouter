package payment

import (
	"context"
	"time"
)

// Payment order pages and dashboards read persisted state through a shared
// query store. The query service retains an injected observation clock and
// provider resolver for document retrieval, while list pagination normalizes
// page size and page numbers before repository reads.

// OrderQueryStore supplies persisted order lists, audit history, and dashboard
// inputs.
type OrderQueryStore interface {
	// OrderAuditLogs returns audit rows for the supplied local order ID. The
	// persistence adapter orders them by creation time; an error is distinct from
	// an order with no audit rows.
	OrderAuditLogs(context.Context, int64) ([]*AuditLog, error)
	// Order returns the persisted payment order identified by local ID.
	Order(context.Context, int64) (*Order, error)
	// GetUserOrders lists orders owned by the supplied user and returns their
	// matching count. Filters and pagination apply to the stored state, with
	// count and page read as separate queries.
	GetUserOrders(context.Context, int64, OrderListParams) ([]*Order, int, error)
	// AdminListOrders lists matching orders and their count for administrator
	// callers. A positive user ID narrows ownership; other IDs retain all users,
	// and keyword filtering applies to stored merchant/profile text.
	AdminListOrders(context.Context, int64, OrderListParams) ([]*Order, int, error)
	// PaidOrders returns supplied statuses whose paid_at falls in the half-open
	// start/end interval. Dashboard callers choose paid, recharging, and
	// completed states; the method itself uses the provided status list.
	PaidOrders(context.Context, time.Time, time.Time, []string) ([]*Order, error)
	// PendingCount returns the total pending and provider-processing order count.
	PendingCount(context.Context) (int, error)
	// PlanNames returns current plan names for the supplied IDs in one lookup.
	// Missing IDs have no map entry, letting dashboard purchase labels use their
	// recorded name or ID.
	PlanNames(context.Context, []int64) (map[int64]string, error)
}

// OrderQueries reads orders and builds payment documents and dashboard
// statistics.
type OrderQueries struct {
	// provider resolves the adapter used when stored invoice references cannot
	// supply a payment document. NewOrderQueries retains the callback and does
	// not synthesize a resolver when it is nil.
	provider func(context.Context, *Order) (Provider, error)
	store    OrderQueryStore  // order reading persistence
	now      func() time.Time // dashboard observation clock
}

// NewOrderQueries retains the store and optional provider resolver for order
// reads and payment documents. An omitted clock uses time.Now; resolving a
// document through the provider requires a usable resolver.
func NewOrderQueries(store OrderQueryStore, now func() time.Time, provider func(context.Context, *Order) (Provider, error)) *OrderQueries {
	if now == nil {
		now = time.Now
	}
	return &OrderQueries{store: store, now: now, provider: provider}
}

// QueryPagination returns the effective list size and one-based page number.
// Nonpositive size selects twenty, size above one hundred is capped, and a
// page below one becomes one.
func QueryPagination(pageSize, page int) (size, pg int) {
	size = pageSize
	if size <= 0 {
		size = 20
	}
	// Bound page reads before computing their offset.
	if size > 100 {
		size = 100
	}
	pg = max(page, 1)
	return
}
