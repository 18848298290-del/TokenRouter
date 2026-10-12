package failover

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	// FailoverContinue asks the caller to repeat the loop on the same or another provider.
	FailoverContinue FailoverAction = iota
	// FailoverExhausted asks the caller to return the final error response.
	FailoverExhausted
	// FailoverCanceled asks the caller to stop after cancellation.
	FailoverCanceled

	// MaxSameProviderRetries is the default same-provider retry limit.
	// Callers may override it with the provider pool retry count.
	MaxSameProviderRetries = 3
	// SameProviderRetryDelay is the default interval between attempts on one provider.
	SameProviderRetryDelay = 500 * time.Millisecond
	// MaxRequestScopedRetryDelay caps exponential backoff for request-scoped transient failures.
	MaxRequestScopedRetryDelay = 8 * time.Second
	// SingleProviderBackoffDelay is the delay before retrying selection after a 503.
	// This outer delay is separate from retries within the upstream executor.

	SingleProviderBackoffDelay = 2 * time.Second
)

// FailureInfo contains the retry and cache-billing decisions for a failed attempt.
type FailureInfo struct {
	StatusCode                int
	ForceCacheBilling         bool
	RetryableOnSameProvider   bool
	RequestScopedTransient    bool
	SameProviderRetryDelay    time.Duration
	SameProviderRetryDeadline time.Time
	SameProviderRetryMax      int
	RetryNext                 bool
}
type Failure interface {
	comparable
	RetryFailure() *FailureInfo
}
type Observe func(context.Context, string, map[string]any)

// TempUnscheduler handles temporary exclusion after same-provider retries are exhausted.
// The execution adapter supplies the provider-specific policy implementation.
type TempUnscheduler[E Failure] interface {
	TempUnscheduleRetryableError(ctx context.Context, providerID int64, failoverErr E)
}

// FailoverAction identifies the next step after failure handling.
type FailoverAction int

// FailoverState carries retry counts and exclusions across request-loop iterations.
type FailoverState[E Failure] struct {
	observe                Observe
	SwitchCount            int
	MaxSwitches            int
	FailedProviderIDs      map[int64]struct{}
	SameProviderRetryCount map[int64]int
	LastFailoverErr        E
	ForceCacheBilling      bool
	HasBoundSession        bool
}

// SameProviderRetryDelayFor honors an explicit delay, otherwise using capped exponential backoff
// for request-scoped transient errors and 500 ms for other failures.
func SameProviderRetryDelayFor(failoverErr *FailureInfo, retryCount int) time.Duration {
	if failoverErr != nil && failoverErr.SameProviderRetryDelay > 0 {
		return failoverErr.SameProviderRetryDelay
	}
	if failoverErr == nil || !failoverErr.RequestScopedTransient || retryCount <= 1 {
		return SameProviderRetryDelay
	}

	delay := SameProviderRetryDelay
	for i := 1; i < retryCount; i++ {
		if delay >= MaxRequestScopedRetryDelay/2 {
			return MaxRequestScopedRetryDelay
		}
		delay *= 2
	}
	return delay
}

func SameProviderRetryAllowed(failoverErr *FailureInfo, retryCount, retryLimit int) bool {
	if failoverErr == nil || !failoverErr.RetryableOnSameProvider {
		return false
	}
	if !SameProviderRetryDeadlineAllows(failoverErr) {
		return false
	}
	// An error-specific retry cap still applies when the error also supplies a deadline.
	if failoverErr.SameProviderRetryMax > 0 {
		if retryLimit <= 0 {
			return false
		}
		if failoverErr.SameProviderRetryMax < retryLimit {
			retryLimit = failoverErr.SameProviderRetryMax
		}
		return retryCount < retryLimit
	}
	// A deadline without an error-specific cap uses a time window instead of the ordinary pool count.
	if !failoverErr.SameProviderRetryDeadline.IsZero() {
		return true
	}
	return retryLimit > 0 && retryCount < retryLimit
}

// SameProviderRetryDeadlineAllows reports whether the supplied deadline is absent or still in the future.
func SameProviderRetryDeadlineAllows(failoverErr *FailureInfo) bool {
	return failoverErr == nil || failoverErr.SameProviderRetryDeadline.IsZero() || time.Now().Before(failoverErr.SameProviderRetryDeadline)
}

// NewFailoverState initializes request-local retry counts and provider exclusions.
func NewFailoverState[E Failure](maxSwitches int, hasBoundSession bool, observers ...Observe) *FailoverState[E] {
	var observe Observe
	if len(observers) > 0 {
		observe = observers[0]
	}
	return &FailoverState[E]{
		observe:                observe,
		MaxSwitches:            maxSwitches,
		FailedProviderIDs:      make(map[int64]struct{}),
		SameProviderRetryCount: make(map[int64]int),
		HasBoundSession:        hasBoundSession,
	}
}

// HandleFailoverError updates retry state and returns the next action.
// It applies cache billing, same-provider retry, temporary exclusion, and platform switch delays.
func (s *FailoverState[E]) HandleFailoverError(
	ctx context.Context,
	gatewayService TempUnscheduler[E],
	providerID int64,
	platform string,
	retryLimit int,
	failoverErr E,
) FailoverAction {
	// Cancellation ends failover before reselection can misclassify the request as provider exhaustion.

	if ctx != nil && ctx.Err() != nil {
		return FailoverCanceled
	}
	s.LastFailoverErr = failoverErr
	failure := failoverErr.RetryFailure()
	if failure == nil || !failure.RetryNext {
		return FailoverExhausted
	}

	// A same-provider retry does not trigger sticky-session switch billing.
	retryCount := s.SameProviderRetryCount[providerID]
	sameProviderRetry := SameProviderRetryAllowed(failure, retryCount, retryLimit)
	if NeedForceCacheBilling(s.HasBoundSession, failure, sameProviderRetry) {
		s.ForceCacheBilling = true
	}

	// Retry eligible transient failures on the same provider before excluding it.
	// The caller supplies the provider pool retry limit.
	if sameProviderRetry {
		s.SameProviderRetryCount[providerID]++
		retryDelay := SameProviderRetryDelayFor(failure, s.SameProviderRetryCount[providerID])
		s.emit(ctx, "gateway.failover_same_provider_retry", map[string]any{"provider_id": providerID, "upstream_status": failure.StatusCode, "same_provider_retry_count": s.SameProviderRetryCount[providerID], "same_provider_retry_max": retryLimit, "retry_delay": retryDelay})
		if !SleepWithContext(ctx, retryDelay) {
			return FailoverCanceled
		}
		return FailoverContinue
	}

	// Apply temporary exclusion when a retryable error cannot retry on this provider.
	if failure.RetryableOnSameProvider {
		gatewayService.TempUnscheduleRetryableError(ctx, providerID, failoverErr)
	}

	s.FailedProviderIDs[providerID] = struct{}{}

	if s.SwitchCount >= s.MaxSwitches {
		return FailoverExhausted
	}

	s.SwitchCount++
	s.emit(ctx, "gateway.failover_switch_provider", map[string]any{"provider_id": providerID, "upstream_status": failure.StatusCode, "switch_count": s.SwitchCount, "max_switches": s.MaxSwitches})

	// Antigravity switch delays grow linearly with the switch count.
	if platform == capability.PlatformAntigravity {
		delay := time.Duration(s.SwitchCount-1) * time.Second
		if !SleepWithContext(ctx, delay) {
			return FailoverCanceled
		}
	}

	return FailoverContinue
}

// HandleSelectionExhausted reopens selection after a 503 while the switch budget allows it.
// It waits, then clears provider exclusions. Callers decide which paths use this recovery.

// On FailoverContinue, callers set their single-provider retry context and repeat selection.
// FailoverExhausted requires an error response.
// FailoverCanceled requires ending the request.
func (s *FailoverState[E]) HandleSelectionExhausted(ctx context.Context) FailoverAction {
	// Treat canceled selection as cancellation rather than provider exhaustion.

	if ctx.Err() != nil {
		return FailoverCanceled
	}

	failure := s.LastFailoverErr.RetryFailure()
	if failure != nil &&
		failure.StatusCode == 503 &&
		s.SwitchCount <= s.MaxSwitches {
		s.emit(ctx, "gateway.failover_single_provider_backoff", map[string]any{"backoff_delay": SingleProviderBackoffDelay, "switch_count": s.SwitchCount, "max_switches": s.MaxSwitches})
		if !SleepWithContext(ctx, SingleProviderBackoffDelay) {
			return FailoverCanceled
		}
		s.emit(ctx, "gateway.failover_single_provider_retry", map[string]any{"switch_count": s.SwitchCount, "max_switches": s.MaxSwitches})
		s.FailedProviderIDs = make(map[int64]struct{})
		return FailoverContinue
	}
	return FailoverExhausted
}

// NeedForceCacheBilling reports whether uncached input should be billed as cache reads.
// A sticky-session provider switch or an explicit upstream flag enables this treatment.
func NeedForceCacheBilling(hasBoundSession bool, failoverErr *FailureInfo, sameProviderRetry bool) bool {
	return (hasBoundSession && !sameProviderRetry) || (failoverErr != nil && failoverErr.ForceCacheBilling)
}

// SleepWithContext waits for a positive duration and returns false if canceled while waiting; nonpositive durations return true.
func SleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (s *FailoverState[E]) emit(ctx context.Context, event string, fields map[string]any) {
	if s.observe != nil {
		s.observe(ctx, event, fields)
	}
}

// EffectiveSameProviderRetryLimit applies a smaller positive error-specific cap to the provider limit.
func EffectiveSameProviderRetryLimit(failure *FailureInfo, limit int) int {
	if limit > 0 && failure != nil && failure.SameProviderRetryMax > 0 && failure.SameProviderRetryMax < limit {
		return failure.SameProviderRetryMax
	}
	return limit
}
