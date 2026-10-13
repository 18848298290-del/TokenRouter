package app

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/google/uuid"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Payment maintenance reconciles pending, processing, and fulfillment work
// before expiring old orders through OrderLifecycle. Assembly supplies a
// one-minute runner and singleton lease identity, while step-specific
// diagnostics report positive work counts and the error severity chosen for
// each phase.

// providePaymentExpiry constructs one-minute order maintenance using the
// assembled order lifecycle. A generated owner identifies lease attempts; the
// Redis leader capability and optional SQL advisory callback are supplied to
// the common singleton helper before each cycle.
func providePaymentExpiry(runtime *payment.Runtime, cache provider.CNMonitorLeader, db *sql.DB) *payment.OrderExpiry {
	owner := uuid.NewString()
	var advisory func(context.Context, string) (func(), bool)
	if db != nil {
		advisory = func(ctx context.Context, key string) (func(), bool) {
			return postgresinfra.TryAcquireDBAdvisoryLock(ctx, db, postgresinfra.HashAdvisoryLockID(key))
		}
	}
	runner := payment.NewOrderExpiry(runtime.OrderLifecycle, time.Minute, payment.ExpiryRuntime{Acquire: func(ctx context.Context) (func(), bool) {
		return provider.AcquireSingletonLease(ctx, cache, advisory, payment.OrderExpiryLeaderKey, owner, payment.OrderExpiryLeaderTTL)
	}, Observe: observePaymentExpiryRuntime})
	return runner
}

// observePaymentExpiryRuntime reports recognized maintenance steps and their
// outcome. Errors suppress count reporting, nonpositive successful counts
// produce no log, and unrecognized step names are ignored by both diagnostic
// switches.
func observePaymentExpiryRuntime(step string, count int, err error) {
	if err != nil {
		switch step {
		case "pending":
			slog.Warn("[PaymentOrderExpiry] failed to reconcile pending payment orders", "error", err)
		case "processing":
			slog.Warn("[PaymentOrderExpiry] failed to reconcile processing payment orders", "error", err)
		case "fulfillment":
			slog.Warn("[PaymentOrderExpiry] failed to reconcile paid order fulfillment", "error", err)
		case "expire":
			slog.Error("[PaymentOrderExpiry] failed to expire orders", "error", err)
		}
		return
	}
	if count <= 0 {
		return
	}
	switch step {
	case "pending":
		slog.Info("[PaymentOrderExpiry] reconciled paid orders", "count", count)
	case "processing":
		slog.Info("[PaymentOrderExpiry] reconciled paid processing orders", "count", count)
	case "fulfillment":
		slog.Info("[PaymentOrderExpiry] reconciled paid order fulfillment", "count", count)
	case "expire":
		slog.Info("[PaymentOrderExpiry] expired timed-out orders", "count", count)
	}
}
