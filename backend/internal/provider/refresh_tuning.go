package provider

import (
	"time"
)

const (
	// TokenRefreshTempUnschedDuration is the scheduling cooldown after refresh retries are exhausted.
	TokenRefreshTempUnschedDuration = 10 * time.Minute

	DefaultTokenRefreshCandidatePageSize        = 200
	MaxTokenRefreshCandidatePageSize            = 1000
	DefaultTokenRefreshProviderConcurrency      = 4
	MaxTokenRefreshProviderConcurrency          = 32
	DefaultTokenRefreshProviderQPS              = 2
	MaxTokenRefreshProviderQPS                  = 100
	DefaultTokenRefreshProviderFailureThreshold = 3
	MaxTokenRefreshProviderFailureThreshold     = 100
	DefaultTokenRefreshMaxRetries               = 1
	MaxTokenRefreshMaxRetries                   = 10
	MaxTokenRefreshRetryBackoff                 = 30 * time.Second
	DefaultTokenRefreshAttemptTimeout           = 15 * time.Second
	MaxTokenRefreshAttemptTimeout               = 5 * time.Minute
	MaxTokenRefreshLockSafetyMargin             = 5 * time.Second
	DefaultTokenRefreshCycleTimeout             = 4 * time.Minute
	MaxTokenRefreshCycleTimeout                 = time.Hour
	DefaultTokenRefreshCleanupTimeout           = 2 * time.Second
)

// RefreshTuning holds application-supplied background refresh parameters.
type RefreshTuning struct {
	Enabled                  bool
	CheckIntervalMinutes     int
	RefreshBeforeExpiryHours float64
	MaxRetries               int
	RetryBackoffSeconds      int
	CandidatePageSize        int
	ProviderConcurrency      int
	ProviderQPS              int
	ProviderFailureThreshold int
	AttemptTimeoutSeconds    int
	CycleTimeoutSeconds      int
}

func (s *RefreshTuning) PageSize() int {
	if s != nil && s.CandidatePageSize > 0 {
		return min(s.CandidatePageSize, MaxTokenRefreshCandidatePageSize)
	}
	return DefaultTokenRefreshCandidatePageSize
}

func (s *RefreshTuning) Concurrency() int {
	if s != nil && s.ProviderConcurrency > 0 {
		return min(s.ProviderConcurrency, MaxTokenRefreshProviderConcurrency)
	}
	return DefaultTokenRefreshProviderConcurrency
}

func (s *RefreshTuning) QPS() int {
	if s != nil && s.ProviderQPS > 0 {
		return min(s.ProviderQPS, MaxTokenRefreshProviderQPS)
	}
	return DefaultTokenRefreshProviderQPS
}

func (s *RefreshTuning) FailureThreshold() int {
	if s != nil && s.ProviderFailureThreshold > 0 {
		return min(s.ProviderFailureThreshold, MaxTokenRefreshProviderFailureThreshold)
	}
	return DefaultTokenRefreshProviderFailureThreshold
}

func (s *RefreshTuning) CycleTimeout() time.Duration {
	if s != nil && s.CycleTimeoutSeconds > 0 {
		seconds := min(s.CycleTimeoutSeconds, int(MaxTokenRefreshCycleTimeout/time.Second))
		return time.Duration(seconds) * time.Second
	}
	return DefaultTokenRefreshCycleTimeout
}

func (s *RefreshTuning) Retries() int {
	if s != nil && s.MaxRetries > 0 {
		return min(s.MaxRetries, MaxTokenRefreshMaxRetries)
	}
	return DefaultTokenRefreshMaxRetries
}

func (s *RefreshTuning) RetryBackoff(providerID int64, attempt int) time.Duration {
	if s == nil || s.RetryBackoffSeconds <= 0 {
		return 0
	}
	shift := min(attempt-1, 10)
	baseSeconds := min(s.RetryBackoffSeconds, int(MaxTokenRefreshRetryBackoff/time.Second))
	base := time.Duration(baseSeconds) * time.Second * time.Duration(1<<shift)
	// Jitter varies from 75% to 125% using provider ID and attempt, spreading provider retries reproducibly.
	jitterPercent := int64(75) + (providerID+int64(attempt*17))%51
	backoff := base * time.Duration(jitterPercent) / 100
	return min(backoff, MaxTokenRefreshRetryBackoff)
}

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

func ClampRefreshAttemptToLockLease(timeout, lease time.Duration) time.Duration {
	if timeout <= 0 || lease <= 0 {
		return timeout
	}
	margin := min(lease/10, MaxTokenRefreshLockSafetyMargin)
	if margin <= 0 {
		margin = time.Nanosecond
	}
	leaseBudget := lease - margin
	if leaseBudget <= 0 {
		leaseBudget = lease / 2
	}
	if leaseBudget > 0 && timeout > leaseBudget {
		return leaseBudget
	}
	return timeout
}
