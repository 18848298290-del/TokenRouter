package failover

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A retryable upstream failure can keep the same provider, exclude it for
// reselection, or end the request when the switch budget is spent.
// Same-provider eligibility combines a retry opt-in with an optional time deadline
// and an error-specific count cap; a deadline alone replaces the ordinary
// provider count window.
//
// Retry counts and exclusions stay local to one request loop. A bound session
// changes cache billing when the next attempt switches provider, while an
// explicit error flag can enable that billing even on a same-provider retry.
// Cancellation interrupts waits and ends recovery; temporary unscheduling is
// delegated before a provider enters the failed set. Selection exhaustion can
// reopen that set after a bounded-delay 503 recovery.

// Failover actions drive the outer request loop; retry durations and limits
// govern waits within one provider and recovery after the candidate set is
// exhausted.
const (
	// FailoverContinue asks the caller to repeat the loop on the same or another
	// provider.
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
	// MaxRequestScopedRetryDelay caps exponential backoff for request-scoped
	// transient failures.
	MaxRequestScopedRetryDelay = 8 * time.Second
	// SingleProviderBackoffDelay is the delay before retrying selection after a 503.
	// This outer delay is separate from retries within the upstream executor.
	SingleProviderBackoffDelay = 2 * time.Second
)

// FailureInfo contains the retry and cache-billing decisions for a failed attempt.
type FailureInfo struct {
	StatusCode              int  // classified upstream HTTP status
	ForceCacheBilling       bool // explicit cache-read billing request
	RetryableOnSameProvider bool // same-provider retry permission flag
	RequestScopedTransient  bool // request-local transient backoff classification
	// SameProviderRetryDelay overrides computed backoff when positive. That supplied
	// duration is used directly, including values above the exponential delay cap.
	SameProviderRetryDelay time.Duration // explicit retry wait duration
	// SameProviderRetryDeadline and SameProviderRetryMax jointly constrain retries
	// when a positive maximum is supplied. A deadline alone replaces the ordinary
	// count limit; an elapsed deadline always refuses another same-provider attempt.
	SameProviderRetryDeadline time.Time // optional retry admission deadline
	SameProviderRetryMax      int       // optional error-specific retry count cap
	RetryNext                 bool      // permission to continue outer recovery
}

// Failure represents comparable upstream errors with request-loop recovery
// decisions.
type Failure interface {
	comparable
	// RetryFailure returns this error's retry decision, with nil representing an
	// unusable failure. Implementations used as the state's zero error must allow
	// this method to be called before a failed attempt has been recorded.
	RetryFailure() *FailureInfo
}

// Observe receives synchronous recovery events and fields from the request
// loop.
type Observe func(context.Context, string, map[string]any)

// TempUnscheduler handles temporary exclusion after same-provider retries are
// exhausted.
// The execution adapter supplies the provider-specific policy implementation.
type TempUnscheduler[E Failure] interface {
	// TempUnscheduleRetryableError applies provider-specific cooldown after a retryable
	// failure exhausts its same-provider opportunity. HandleFailoverError calls it
	// before adding that provider to the request's failed selection set.
	TempUnscheduleRetryableError(ctx context.Context, providerID int64, failoverErr E)
}

// FailoverAction tells the outer request loop whether to continue, fail, or
// cancel.
type FailoverAction int

// FailoverState retains counts, exclusions, and billing decisions for one
// request. Its caller serializes access across selection and failure handling;
// the state supplies no synchronization for concurrent loops.
type FailoverState[E Failure] struct {
	observe Observe // optional synchronous recovery event observer
	// SwitchCount and MaxSwitches count outer provider changes. Same-provider
	// retries keep this count unchanged; reaching the maximum ends a further switch.
	SwitchCount            int                // provider switches already consumed
	MaxSwitches            int                // configured provider switch allowance
	FailedProviderIDs      map[int64]struct{} // request-local failed provider exclusions
	SameProviderRetryCount map[int64]int      // retry counts by provider identifier
	LastFailoverErr        E                  // last classified upstream failure
	// ForceCacheBilling stays enabled after any failure requires cache-read billing,
	// so subsequent attempts retain the request's accumulated billing decision.
	ForceCacheBilling bool // accumulated cache-read billing requirement
	HasBoundSession   bool // request started with sticky binding
}

// SameProviderRetryDelayFor returns the wait before the numbered same-provider
// retry. A positive error-supplied duration wins directly; otherwise the first
// retry and ordinary failures use the default half-second interval.
//
// Request-scoped transient retries double that interval for each later retry
// until eight seconds is reached. The explicit duration is outside that cap,
// so callers choosing it control their full wait. Admission checks and
// cancellation happen separately; calculating a delay does not grant another
// attempt.
func SameProviderRetryDelayFor(failoverErr *FailureInfo, retryCount int) time.Duration {
	if failoverErr != nil && failoverErr.SameProviderRetryDelay > 0 {
		return failoverErr.SameProviderRetryDelay
	}
	if failoverErr == nil || !failoverErr.RequestScopedTransient || retryCount <= 1 {
		return SameProviderRetryDelay
	}

	// Stop doubling before the next multiplication reaches the cap, keeping later
	// retry counts at eight seconds instead of allowing duration growth to overflow.
	delay := SameProviderRetryDelay
	for i := 1; i < retryCount; i++ {
		if delay >= MaxRequestScopedRetryDelay/2 {
			return MaxRequestScopedRetryDelay
		}
		delay *= 2
	}
	return delay
}

// SameProviderRetryAllowed reports whether the failure permits another
// same-provider attempt at retryCount. A missing failure, missing retry opt-in, or
// elapsed deadline rejects the attempt before count limits are considered.
//
// A positive error-specific maximum requires a positive caller limit and
// narrows it when smaller. A deadline without that maximum uses the remaining
// time window even when the ordinary count limit is zero or exhausted. Without
// either special rule, the positive caller limit governs admission using a
// strict retryCount comparison.
func SameProviderRetryAllowed(failoverErr *FailureInfo, retryCount, retryLimit int) bool {
	if failoverErr == nil || !failoverErr.RetryableOnSameProvider {
		return false
	}
	if !SameProviderRetryDeadlineAllows(failoverErr) {
		return false
	}
	// Keep an explicit error cap alongside a deadline, so a future time window
	// cannot bypass the caller's zero budget or a smaller permitted retry count.
	if failoverErr.SameProviderRetryMax > 0 {
		if retryLimit <= 0 {
			return false
		}
		if failoverErr.SameProviderRetryMax < retryLimit {
			retryLimit = failoverErr.SameProviderRetryMax
		}
		return retryCount < retryLimit
	}
	// An uncapped deadline grants retries by time alone, allowing the transient
	// request window to continue even after the provider's ordinary count is spent.
	if !failoverErr.SameProviderRetryDeadline.IsZero() {
		return true
	}
	return retryLimit > 0 && retryCount < retryLimit
}

// SameProviderRetryDeadlineAllows reports whether the retry deadline is absent
// or later than the current wall clock. Nil failures and zero deadlines pass;
// equality at the deadline fails.
func SameProviderRetryDeadlineAllows(failoverErr *FailureInfo) bool {
	return failoverErr == nil || failoverErr.SameProviderRetryDeadline.IsZero() || time.Now().Before(failoverErr.SameProviderRetryDeadline)
}

// NewFailoverState creates empty request-local retry and exclusion maps with
// the supplied switch budget and sticky flag. It retains the first optional
// observer; negative switch budgets are stored as supplied.
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

// HandleFailoverError records the failure, accumulates cache billing, and
// returns the next outer-loop action. Callers supply a usable error and
// serialize this state; a nonnil context is required for waits, and retryable
// exhaustion requires a usable temporary-unscheduling adapter.
//
// Same-provider admission is resolved before billing and exclusion so a
// retained sticky provider does not automatically trigger switch billing.
// Admitted retries increment only their provider counter and wait with
// cancellation. Otherwise the adapter applies retryable cooldown, the provider
// enters the failed set, and the outer switch count advances when budget
// remains. Antigravity adds a switch-count delay before continuing.
func (s *FailoverState[E]) HandleFailoverError(
	ctx context.Context,
	gatewayService TempUnscheduler[E],
	providerID int64,
	platform string,
	retryLimit int,
	failoverErr E,
) FailoverAction {
	// End a canceled request before recording another provider failure or entering
	// reselection, keeping client cancellation distinct from candidate exhaustion.
	if ctx != nil && ctx.Err() != nil {
		return FailoverCanceled
	}
	s.LastFailoverErr = failoverErr
	failure := failoverErr.RetryFailure()
	if failure == nil || !failure.RetryNext {
		return FailoverExhausted
	}

	// Resolve whether the sticky provider is retained before accumulating billing,
	// because ordinary bound-session cache compensation starts when the attempt
	// switches.
	retryCount := s.SameProviderRetryCount[providerID]
	sameProviderRetry := SameProviderRetryAllowed(failure, retryCount, retryLimit)
	if NeedForceCacheBilling(s.HasBoundSession, failure, sameProviderRetry) {
		s.ForceCacheBilling = true
	}

	// Keep an admitted provider available while its retry counter advances; outer
	// exclusions and switch counts remain unchanged until this retry opportunity ends.
	if sameProviderRetry {
		s.SameProviderRetryCount[providerID]++
		retryDelay := SameProviderRetryDelayFor(failure, s.SameProviderRetryCount[providerID])
		s.emit(ctx, "gateway.failover_same_provider_retry", map[string]any{"provider_id": providerID, "upstream_status": failure.StatusCode, "same_provider_retry_count": s.SameProviderRetryCount[providerID], "same_provider_retry_max": retryLimit, "retry_delay": retryDelay})
		if !SleepWithContext(ctx, retryDelay) {
			return FailoverCanceled
		}
		return FailoverContinue
	}

	// Apply the adapter's retryable cooldown before excluding the provider locally,
	// so later selection sees the health decision when this retry opportunity ends.
	if failure.RetryableOnSameProvider {
		gatewayService.TempUnscheduleRetryableError(ctx, providerID, failoverErr)
	}

	s.FailedProviderIDs[providerID] = struct{}{}

	if s.SwitchCount >= s.MaxSwitches {
		return FailoverExhausted
	}

	s.SwitchCount++
	s.emit(ctx, "gateway.failover_switch_provider", map[string]any{"provider_id": providerID, "upstream_status": failure.StatusCode, "switch_count": s.SwitchCount, "max_switches": s.MaxSwitches})

	// Pace Antigravity reselection using the consumed outer switch count; the first
	// switch has no delay, and cancellation stops a later scheduled wait.
	if platform == capability.PlatformAntigravity {
		delay := time.Duration(s.SwitchCount-1) * time.Second
		if !SleepWithContext(ctx, delay) {
			return FailoverCanceled
		}
	}

	return FailoverContinue
}

// HandleSelectionExhausted returns cancellation, exhaustion, or a reopened
// selection action after the last failure. Callers use a nonnil context and a
// Failure implementation whose zero value tolerates RetryFailure, because
// selection can fail before an upstream error exists.
//
// A recorded 503 permits a two-second wait when the consumed switch count has
// not exceeded its maximum. Successful waiting replaces the failed-provider
// set so the caller can select that provider again. Retry counts and the
// switch count remain available; the caller repeats selection with its
// single-provider retry context and advances recovery through subsequent failure
// handling.
func (s *FailoverState[E]) HandleSelectionExhausted(ctx context.Context) FailoverAction {
	// Preserve the request's cancellation result before interpreting its stored
	// failure; a canceled selection must not reopen the provider set for recovery.
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
		// Reopen the candidate set after the wait succeeds; retained counters still
		// constrain subsequent failures even though previously excluded IDs can reenter.
		s.FailedProviderIDs = make(map[int64]struct{})
		return FailoverContinue
	}
	return FailoverExhausted
}

// NeedForceCacheBilling reports whether ordinary input should receive
// cache-read billing for this failure. A request that started with a bound session
// enables this compensation when the next attempt leaves that provider.
//
// An admitted same-provider retry keeps the ordinary sticky compensation off,
// because that retry retains its cache location. The failure's explicit
// ForceCacheBilling flag enables the result independently, including on a
// same-provider retry or an initially unbound request. Callers accumulate a
// true result across the request's later attempts.
func NeedForceCacheBilling(hasBoundSession bool, failoverErr *FailureInfo, sameProviderRetry bool) bool {
	return (hasBoundSession && !sameProviderRetry) || (failoverErr != nil && failoverErr.ForceCacheBilling)
}

// SleepWithContext waits for a positive duration and reports whether its timer
// wins over cancellation. Nonpositive durations return true immediately, even
// for canceled contexts; positive waits require a nonnil context.
func SleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	// Cancellation interrupts the caller's backoff so another upstream attempt
	// cannot start merely because its normal retry delay has yet to elapse.
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// emit synchronously forwards a recovery event when an observer is configured.
// The observer receives the supplied fields map and runs inside the caller's
// request loop.
func (s *FailoverState[E]) emit(ctx context.Context, event string, fields map[string]any) {
	if s.observe != nil {
		s.observe(ctx, event, fields)
	}
}

// EffectiveSameProviderRetryLimit narrows a positive provider limit to a
// smaller positive error cap. Missing caps, nil failures, and nonpositive
// provider limits retain the supplied limit unchanged.
func EffectiveSameProviderRetryLimit(failure *FailureInfo, limit int) int {
	if limit > 0 && failure != nil && failure.SameProviderRetryMax > 0 && failure.SameProviderRetryMax < limit {
		return failure.SameProviderRetryMax
	}
	return limit
}
