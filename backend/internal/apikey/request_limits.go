package apikey

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
)

var (
	// ErrKeyConcurrencyExceeded reports that the key has reached its active request limit.
	ErrKeyConcurrencyExceeded = errors.New("API key concurrency limit exceeded")
	// ErrKeyRPMExceeded reports that the key has reached its rolling one-minute request limit.
	ErrKeyRPMExceeded = errors.New("API key RPM limit exceeded")
	// ErrKeyLimiterUnavailable reports an unavailable request-limit store or lease.
	ErrKeyLimiterUnavailable = errors.New("API key request limiter unavailable")
)

// RequestLimitCache atomically reserves request capacity and maintains active request leases.
type RequestLimitCache interface {
	ReserveRequest(context.Context, int64, string, int, int) (time.Duration, error)
	RefreshRequest(context.Context, int64, string) error
	ReleaseRequest(context.Context, int64, string) error
}

// ValidateRequestLimits checks nonnegative limits within the PostgreSQL integer range.
func ValidateRequestLimits(concurrency, rpm *int) error {
	for field, value := range map[string]*int{"concurrency_limit": concurrency, "rpm_limit": rpm} {
		if value != nil && (*value < 0 || int64(*value) > math.MaxInt32) {
			return ErrAPIKeyLimitInvalid.WithMetadata(map[string]string{"field": field})
		}
	}
	return nil
}

// AcquireRequest counts RPM at admission and renews the concurrency lease until release is called.
// Renewal failure cancels the returned context; callers must stop processing and release the lease.
// @project-doc docs/domains/routing_and_billing.md#api_key_request_limits
func (s *APIKeyService) AcquireRequest(ctx context.Context, key *APIKey) (context.Context, func(), time.Duration, error) {
	if key.ConcurrencyLimit <= 0 && key.RPMLimit <= 0 {
		return ctx, func() {}, 0, nil
	}
	cache, ok := s.cache.(RequestLimitCache)
	if !ok {
		return ctx, nil, 0, ErrKeyLimiterUnavailable
	}
	if !s.operations.enter() {
		return ctx, nil, 0, ErrKeyLimiterUnavailable
	}
	id := uuid.NewString()
	reserveCtx, cancelReserve := context.WithTimeout(ctx, 3*time.Second)
	retry, err := cache.ReserveRequest(reserveCtx, key.ID, id, key.ConcurrencyLimit, key.RPMLimit)
	cancelReserve()
	if err != nil {
		// Redis may have reserved before losing the response. Clean up using an independent context.
		if !errors.Is(err, ErrKeyConcurrencyExceeded) && !errors.Is(err, ErrKeyRPMExceeded) {
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 3*time.Second)
			_ = cache.ReleaseRequest(releaseCtx, key.ID, id)
			cancelRelease()
		}
		s.operations.leave()
		return ctx, nil, retry, err
	}
	if key.ConcurrencyLimit <= 0 {
		s.operations.leave()
		return ctx, func() {}, 0, nil
	}
	requestCtx, abort := requestcontext.WithAbort(ctx)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// Handlers may collect results after disconnect, so keep renewing until they release the lease.
				refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				err := cache.RefreshRequest(refreshCtx, key.ID, id)
				cancelRefresh()
				if err != nil {
					abort()
					return
				}
			}
		}
	}()
	var once sync.Once
	release := func() {
		once.Do(func() {
			defer s.operations.leave()
			close(stop)
			abort()
			<-done
			// Release after client cancellation using an independent cleanup timeout.
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancelRelease()
			_ = cache.ReleaseRequest(releaseCtx, key.ID, id)
		})
	}
	return requestCtx, release, 0, nil
}
