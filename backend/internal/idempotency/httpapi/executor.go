package httpapi

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
)

// HTTP write handlers share an injected coordinator for idempotency decisions.
// Standalone HTTP fixtures can use a zero-value Executor to invoke callbacks
// directly; production app wiring binds the coordinator before requests begin,
// making its configuration and stored results available to every bound handler.

// Executor holds the injected coordinator. Its zero value executes callbacks directly for HTTP fixtures.
// Call BindIdempotency before serving requests; binding is not synchronized with execution.
type Executor struct {
	// coordinator is bound before serving requests and then read by handlers.
	// BindIdempotency changes this pointer without synchronization, so binding cannot race execution.
	coordinator *idempotency.IdempotencyCoordinator
}

// BindIdempotency assigns the coordinator used by this handler's write helpers.
// Call it before serving requests because concurrent binding and execution are
// unsynchronized; a nil coordinator enables the direct-callback HTTP fixture behavior.
func (e *Executor) BindIdempotency(coordinator *idempotency.IdempotencyCoordinator) {
	e.coordinator = coordinator
}

// DefaultWriteIdempotencyTTL returns the bound coordinator's write retention period.
// Nil coordinators and nonpositive configured periods use the coordinator method's
// default, allowing standalone fixtures to select a usable write TTL too.
func (e *Executor) DefaultWriteIdempotencyTTL() time.Duration {
	return e.coordinator.DefaultWriteIdempotencyTTL()
}

// DefaultSystemOperationIdempotencyTTL returns the bound coordinator's maintenance
// retention period. The delegated method returns its default for a nil coordinator
// or nonpositive configuration, matching maintenance helpers that use the same instance.
func (e *Executor) DefaultSystemOperationIdempotencyTTL() time.Duration {
	return e.coordinator.DefaultSystemOperationIdempotencyTTL()
}
