package provider

import (
	"time"
)

// Background refresh budgets must fit both configured work limits and a
// distributed lock's finite lease. Accessors default absent or nonpositive
// settings, cap administrator limits, and keep total cycle work distinct from
// one attempt's deadline. MaxRetries supplies the total number of attempted
// exchanges, including the first.
//
// Retry spacing combines exponential growth with reproducible
// provider-and-attempt jitter, then applies a final ceiling. Attempt timeout
// selects an override or configured value and can be shortened to leave time
// before lease expiry. The lease margin is proportional up to its fixed maximum,
// with duration-rounding handling for tiny positive leases. These calculations
// bound cooperative work; callers and transport implementations still honor the
// resulting contexts.

// Refresh defaults and ceilings bound candidate scanning, platform admission,
// exchange retries and cleanup budgets used by background maintenance.
const (
	// TokenRefreshTempUnschedDuration is the scheduling cooldown after refresh
	// retries are exhausted.
	TokenRefreshTempUnschedDuration = 10 * time.Minute

	// DefaultTokenRefreshCandidatePageSize is the default candidate page size.
	DefaultTokenRefreshCandidatePageSize = 200
	// MaxTokenRefreshCandidatePageSize is the candidate page size ceiling.
	MaxTokenRefreshCandidatePageSize = 1000
	// DefaultTokenRefreshProviderConcurrency is the default occupied platform
	// attempt limit.
	DefaultTokenRefreshProviderConcurrency = 4
	// MaxTokenRefreshProviderConcurrency is the occupied platform attempt
	// ceiling.
	MaxTokenRefreshProviderConcurrency = 32
	// DefaultTokenRefreshProviderQPS is the default exchange starts per second.
	DefaultTokenRefreshProviderQPS = 2
	// MaxTokenRefreshProviderQPS is the platform exchange QPS ceiling.
	MaxTokenRefreshProviderQPS = 100
	// DefaultTokenRefreshProviderFailureThreshold is the default consecutive
	// cycle failure threshold.
	DefaultTokenRefreshProviderFailureThreshold = 3
	// MaxTokenRefreshProviderFailureThreshold is the cycle failure threshold
	// ceiling.
	MaxTokenRefreshProviderFailureThreshold = 100
	// DefaultTokenRefreshMaxRetries is the default total exchange attempt count.
	DefaultTokenRefreshMaxRetries = 1
	// MaxTokenRefreshMaxRetries is the total exchange attempt ceiling.
	MaxTokenRefreshMaxRetries = 10
	// MaxTokenRefreshRetryBackoff is the maximum delay between attempts.
	MaxTokenRefreshRetryBackoff = 30 * time.Second
	// DefaultTokenRefreshAttemptTimeout is the default individual attempt
	// deadline.
	DefaultTokenRefreshAttemptTimeout = 15 * time.Second
	// MaxTokenRefreshAttemptTimeout is the configured attempt deadline ceiling.
	MaxTokenRefreshAttemptTimeout = 5 * time.Minute
	// MaxTokenRefreshLockSafetyMargin is the maximum margin before lease expiry.
	MaxTokenRefreshLockSafetyMargin = 5 * time.Second
	// DefaultTokenRefreshCycleTimeout is the default complete scan budget.
	DefaultTokenRefreshCycleTimeout = 4 * time.Minute
	// MaxTokenRefreshCycleTimeout is the complete scan budget ceiling.
	MaxTokenRefreshCycleTimeout = time.Hour
	// DefaultTokenRefreshCleanupTimeout is the post-commit
	// cancellation-independent cleanup budget.
	DefaultTokenRefreshCleanupTimeout = 2 * time.Second
)

// RefreshTuning carries administrator settings consumed by background scanning
// and attempt budgets.
type RefreshTuning struct {
	Enabled                  bool    // periodic service startup enabled
	CheckIntervalMinutes     int     // minutes between periodic scans
	RefreshBeforeExpiryHours float64 // hours before credential expiry
	MaxRetries               int     // configured total attempt count
	RetryBackoffSeconds      int     // seconds for initial retry delay
	CandidatePageSize        int     // configured candidate page size
	ProviderConcurrency      int     // configured occupied platform attempt limit
	ProviderQPS              int     // configured exchange starts per second
	ProviderFailureThreshold int     // configured consecutive failure trip count
	AttemptTimeoutSeconds    int     // seconds for each attempt budget
	CycleTimeoutSeconds      int     // seconds for complete scan budget
}

// PageSize returns the positive configured page size capped at its scan ceiling.
// A nil receiver or nonpositive setting selects the default page size used by
// candidate scanning.
func (s *RefreshTuning) PageSize() int {
	if s != nil && s.CandidatePageSize > 0 {
		return min(s.CandidatePageSize, MaxTokenRefreshCandidatePageSize)
	}
	return DefaultTokenRefreshCandidatePageSize
}

// Concurrency returns the positive platform attempt limit capped at its
// configured ceiling. A nil receiver or nonpositive setting selects the default
// shared capacity and worker count.
func (s *RefreshTuning) Concurrency() int {
	if s != nil && s.ProviderConcurrency > 0 {
		return min(s.ProviderConcurrency, MaxTokenRefreshProviderConcurrency)
	}
	return DefaultTokenRefreshProviderConcurrency
}

// QPS returns the positive exchange-start rate capped at the platform QPS
// ceiling. A nil receiver or nonpositive setting selects the default rate
// supplied when platform gates are first created.
func (s *RefreshTuning) QPS() int {
	if s != nil && s.ProviderQPS > 0 {
		return min(s.ProviderQPS, MaxTokenRefreshProviderQPS)
	}
	return DefaultTokenRefreshProviderQPS
}

// FailureThreshold returns the configured positive streak threshold capped at
// its maximum. A nil receiver or nonpositive setting selects the default used to
// trip a platform execution state.
func (s *RefreshTuning) FailureThreshold() int {
	if s != nil && s.ProviderFailureThreshold > 0 {
		return min(s.ProviderFailureThreshold, MaxTokenRefreshProviderFailureThreshold)
	}
	return DefaultTokenRefreshProviderFailureThreshold
}

// CycleTimeout returns the positive configured scan duration capped at one hour.
// A nil receiver or nonpositive setting selects the default budget enclosing
// candidate paging and processing.
func (s *RefreshTuning) CycleTimeout() time.Duration {
	if s != nil && s.CycleTimeoutSeconds > 0 {
		seconds := min(s.CycleTimeoutSeconds, int(MaxTokenRefreshCycleTimeout/time.Second))
		return time.Duration(seconds) * time.Second
	}
	return DefaultTokenRefreshCycleTimeout
}

// Retries returns the total allowed attempts, including the first exchange,
// capped at the configured maximum. A nil receiver or nonpositive setting
// selects the default single attempt.
func (s *RefreshTuning) Retries() int {
	if s != nil && s.MaxRetries > 0 {
		return min(s.MaxRetries, MaxTokenRefreshMaxRetries)
	}
	return DefaultTokenRefreshMaxRetries
}

// RetryBackoff returns the capped, reproducibly jittered delay before the next
// exchange attempt. Nil tuning or nonpositive backoff disables waiting; reached
// callers supply a positive attempt and a provider ID whose jitter addition is
// representable.
//
// The configured seconds are capped before exponential growth, and the shift
// stops growing after ten. With a nonnegative jitter input, the provider and
// attempt select a percentage from 75 through 125, spreading starts while
// keeping fixture timing reproducible. A final cap limits the resulting wait
// even after exponential growth and jitter.
func (s *RefreshTuning) RetryBackoff(providerID int64, attempt int) time.Duration {
	if s == nil || s.RetryBackoffSeconds <= 0 {
		return 0
	}
	// Cap exponential growth so later attempts keep the same multiplier; the
	// final delay ceiling still applies after provider-specific jitter is added.
	shift := min(attempt-1, 10)
	baseSeconds := min(s.RetryBackoffSeconds, int(MaxTokenRefreshRetryBackoff/time.Second))
	base := time.Duration(baseSeconds) * time.Second * time.Duration(1<<shift)
	// Provider-and-attempt jitter spreads retry starts reproducibly. For a
	// nonnegative representable sum, the remainder selects 75 through 125 percent
	// before the final backoff ceiling is applied.
	jitterPercent := int64(75) + (providerID+int64(attempt*17))%51
	backoff := base * time.Duration(jitterPercent) / 100
	return min(backoff, MaxTokenRefreshRetryBackoff)
}

// AttemptTimeout chooses an individual attempt budget and optionally limits it
// by the distributed lease. A positive override takes precedence and is used as
// supplied; otherwise positive configured seconds are capped, with the default
// used for absent settings.
//
// When configured is true, ClampRefreshAttemptToLockLease reserves a margin
// before lease expiry and can shorten the chosen budget. Background assembly
// uses the coordinator's LockLease result to decide this condition, so
// local-only refresh retains its chosen budget. The timeout does not renew a
// distributed lease or force an exchange callback to honor cancellation.
func (s *RefreshTuning) AttemptTimeout(override, lease time.Duration, configured bool) time.Duration {
	timeout := DefaultTokenRefreshAttemptTimeout
	if override > 0 {
		timeout = override
	} else if s != nil && s.AttemptTimeoutSeconds > 0 {
		seconds := min(s.AttemptTimeoutSeconds, int(MaxTokenRefreshAttemptTimeout/time.Second))
		timeout = time.Duration(seconds) * time.Second
	}
	if configured {
		timeout = ClampRefreshAttemptToLockLease(timeout, lease)
	}
	return timeout
}

// ClampRefreshAttemptToLockLease shortens a positive attempt budget to leave
// time before a positive distributed lease expires. Nonpositive timeout or lease
// returns the input unchanged, and callers retain responsibility for cooperative
// cancellation.
//
// The margin is one tenth of the lease capped at five seconds, with a
// one-nanosecond minimum when integer duration division produces zero. If
// subtracting the margin leaves no positive budget, half the lease is tried. A
// one-nanosecond lease still has no representable positive remainder, so that
// case leaves timeout unchanged; larger usable budgets clamp only when smaller
// than timeout.
func ClampRefreshAttemptToLockLease(timeout, lease time.Duration) time.Duration {
	if timeout <= 0 || lease <= 0 {
		return timeout
	}
	// Reserve lease time for work outside the cooperative exchange deadline;
	// very small durations need a positive margin after integer rounding.
	margin := min(lease/10, MaxTokenRefreshLockSafetyMargin)
	if margin <= 0 {
		margin = time.Nanosecond
	}
	leaseBudget := lease - margin
	// If the minimum margin consumes the lease, try its representable half.
	// A one-nanosecond lease has no positive half and leaves timeout unchanged.
	if leaseBudget <= 0 {
		leaseBudget = lease / 2
	}
	if leaseBudget > 0 && timeout > leaseBudget {
		return leaseBudget
	}
	return timeout
}
