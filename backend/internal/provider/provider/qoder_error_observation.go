package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// qoder_error_observation.go converts typed Qoder API failures into rate-limit
// or overload observations. Qoder can report an agent limit inside a typed API
// error even when the outer stream began successfully. Health observation
// extracts that error from the chain, chooses its reset deadline or a short
// temporary window, and writes state with a budget detached from the failed
// request.

// QoderHealthStore persists provider-wide limit and overload deadlines from
// failed attempts.
type QoderHealthStore interface {
	// SetRateLimited writes a provider-wide cooldown ending at the supplied
	// deadline.
	// Implementations return persistence errors to the observer, which treats them
	// as best effort for an already failed request.
	SetRateLimited(context.Context, int64, time.Time) error
	// SetOverloaded writes the temporary overload deadline for the supplied
	// provider.
	// Implementations return persistence errors to the observer, which
	// discards them
	// while the caller continues handling the upstream failure.
	SetOverloaded(context.Context, int64, time.Time) error
}

// ObserveQoderUpstreamError records supported Qoder API failures as
// best-effort health state.
// A nil store, nil error or error chain without APIError produces no write.
// Agent limits prefer their parsed reset; other 429 and server errors receive
// thirty-second windows. Writes retain context values with a separate five-second
// budget, and storage errors are discarded.
func ObserveQoderUpstreamError(ctx context.Context, providerID int64, store QoderHealthStore, err error) {
	if store == nil || err == nil {
		return
	}
	var apiErr *qoder.APIError
	if !errors.As(err, &apiErr) {
		return
	}
	// A failed stream can already have cancelled its request. Retain values while
	// giving the health write its own deadline so the next selection can
	// observe it.
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	stateCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	// Health persistence is best effort for this already failed exchange; its
	// storage errors do not replace the upstream error handled by the caller.
	switch {
	case apiErr.IsAgentLimit():
		resetAt, ok := apiErr.AgentLimitResetAt()
		// Agent-limit metadata can omit or fail reset parsing. Use a short window
		// so the scheduler avoids immediate reuse until another request can
		// confirm recovery.
		if !ok {
			resetAt = time.Now().Add(30 * time.Second)
		}
		_ = store.SetRateLimited(stateCtx, providerID, resetAt)
	case apiErr.StatusCode == http.StatusTooManyRequests:
		_ = store.SetRateLimited(stateCtx, providerID, time.Now().Add(30*time.Second))
	case apiErr.StatusCode >= 500:
		_ = store.SetOverloaded(stateCtx, providerID, time.Now().Add(30*time.Second))
	}
}
