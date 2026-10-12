package httpapi

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
)

// Executor holds the injected coordinator. Its zero value executes callbacks directly for HTTP fixtures.
// Call BindIdempotency before serving requests; binding is not synchronized with execution.
type Executor struct {
	coordinator *idempotency.IdempotencyCoordinator
}

func (e *Executor) BindIdempotency(coordinator *idempotency.IdempotencyCoordinator) {
	e.coordinator = coordinator
}

func (e *Executor) DefaultWriteIdempotencyTTL() time.Duration {
	return e.coordinator.DefaultWriteIdempotencyTTL()
}

func (e *Executor) DefaultSystemOperationIdempotencyTTL() time.Duration {
	return e.coordinator.DefaultSystemOperationIdempotencyTTL()
}
