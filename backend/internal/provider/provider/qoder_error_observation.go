package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// QoderHealthStore writes rate-limit and temporary overload state.
type QoderHealthStore interface {
	SetRateLimited(context.Context, int64, time.Time) error
	SetOverloaded(context.Context, int64, time.Time) error
}

// ObserveQoderUpstreamError attempts a health write with a separate five-second cancellation budget.
func ObserveQoderUpstreamError(ctx context.Context, providerID int64, store QoderHealthStore, err error) {
	if store == nil || err == nil {
		return
	}
	var apiErr *qoder.APIError
	if !errors.As(err, &apiErr) {
		return
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	stateCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	switch {
	case apiErr.IsAgentLimit():
		resetAt, ok := apiErr.AgentLimitResetAt()
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
