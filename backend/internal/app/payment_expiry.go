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

// observePaymentExpiryRuntime records counts and errors by maintenance step.
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
