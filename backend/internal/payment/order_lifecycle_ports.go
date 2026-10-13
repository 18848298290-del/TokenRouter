package payment

import (
	"context"
	"sync"
	"time"
)

// Recovery workers need persisted candidates while user cancellation and late
// callbacks can change the same orders. LifeStore supplies those reads and
// conditional closing operations, and OrderLifecycle shares the fulfillment
// service and signer used by the other payment entry points.
//
// Processing and fulfillment scans keep separate page counters under one mutex
// so repeated rounds can reach later IDs. Selection advances a counter before
// downstream work completes, and repositories reread status where their query
// requires it. Constructor binding starts no worker; OrderExpiry later drives
// these operations with its running context and per-phase deadlines. Provider
// measurement returns a completion callback after each observed call.

// OrderLifecycle coordinates provider checks, closing, recovery scans, and
// signed order lookup.
type OrderLifecycle struct {
	*Fulfillment                       // payment confirmation and entitlement service
	resume       *PaymentResumeService // shared payment recovery signer
	// observe measures provider queries and cancellations and returns a nonnil
	// completion callback. NewOrderLifecycle installs the no-op pair when callers
	// omit dependency measurement.
	observe func(context.Context) func()
	// reconcileCursorMu, processingReconcileCursor, and
	// fulfillmentReconcileCursor protect page selection in NextReconcilePageIDs.
	// Each counter advances when multiple pages exist, before the selected page
	// has been queried or processed successfully.
	// reconciliation cursor synchronization mutex
	reconcileCursorMu          sync.Mutex
	processingReconcileCursor  uint64 // processing page selection counter
	fulfillmentReconcileCursor uint64 // fulfillment page selection counter
}

// LifeStore supplies order-closing writes and persisted candidates for payment
// recovery.
type LifeStore interface {
	// ForceExpire atomically marks a currently pending order expired and writes
	// its administrator reason audit. The PostgreSQL adapter owns the transaction
	// and returns not-found or status conflict when the pending update cannot
	// apply. Audit failure returns before commit with deferred rollback cleanup;
	// commit errors propagate to the caller. Callers validate the trimmed reason
	// before this method, and verified later payment can enter normal
	// confirmation from EXPIRED.
	ForceExpire(context.Context, int64, string) error
	// TouchPending refreshes updated_at for an order that is still pending.
	// Cancellation failure uses this timestamp for its retry delay; zero matched
	// rows are accepted by the persistence adapter.
	TouchPending(context.Context, int64, time.Time) error
	// SaveUpstreamTradeNumber persists the upstream transaction identifier on the
	// addressed order. Reconciliation calls it before notification processing,
	// and the adapter update does not condition the write on an order status.
	SaveUpstreamTradeNumber(context.Context, int64, string) error
	// PendingReconciliation reads the oldest unexpired pending Alipay or WeChat
	// method/provider orders, bounded by limit. Each returned order still needs a
	// provider query before its payment can be confirmed.
	PendingReconciliation(context.Context, time.Time, int) ([]*Order, error)
	// ExpiredPending reads pending orders whose expiration is at or before the
	// supplied time. Lifecycle closing still queries any provider state and
	// checks cancellation-failure cooldown before attempting the local
	// transition.
	ExpiredPending(context.Context, time.Time) ([]*Order, error)
	// ProcessingIDs returns all provider processing order IDs in ascending local
	// ID order.
	ProcessingIDs(context.Context) ([]int64, error)
	// ProcessingOrders rereads the selected IDs that remain in provider
	// processing state and orders them by ID. A concurrent state change can
	// remove a candidate between ID selection and this read.
	ProcessingOrders(context.Context, []int64) ([]*Order, error)
	// RecoverableFulfillmentIDs returns ascending IDs with paid_at set whose
	// update time reaches the delay selected by current status. Paid/failed
	// orders use the retry delay, while recharging orders use the lease duration;
	// the persistence adapter uses inclusive time comparisons.
	//
	// These are observations for background page selection. Fulfillment rereads
	// state and then claims paid/failed or stale recharging state separately; its
	// claim does not repeat this query's paid_at or paid/failed retry-delay
	// predicates. Callers retain that distinction when interpreting a selected
	// ID after other order writes.
	RecoverableFulfillmentIDs(context.Context, time.Time, time.Duration, time.Duration) ([]int64, error)
}

// NewOrderLifecycle binds fulfillment and the shared resume service for
// payment recovery. A missing dependency observer returns a no-op completion
// callback; construction leaves reconciliation cursors at zero and starts no
// background work.
func NewOrderLifecycle(core *Fulfillment, resume *PaymentResumeService, observe func(context.Context) func()) *OrderLifecycle {
	if observe == nil {
		observe = func(context.Context) func() { return func() {} }
	}
	return &OrderLifecycle{Fulfillment: core, resume: resume, observe: observe}
}
