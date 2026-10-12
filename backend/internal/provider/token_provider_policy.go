package provider

import (
	"time"
)

// Refresh failures have different request outcomes across OAuth platforms.
// Claude and OpenAI permit a stored-token response with a policy-selected
// cache lifetime, while Gemini, Antigravity and Grok return exchange failures.
// Lock-result policy separately controls whether a token source polls for a
// cache fill before using its captured credentials.

// Refresh-error actions determine whether a token source propagates an exchange
// failure or attempts to return the access token already present in credentials.
const (
	// ProviderRefreshErrorReturn propagates the token exchange failure.
	ProviderRefreshErrorReturn ProviderRefreshErrorAction = iota
	// ProviderRefreshErrorUseExistingToken permits stored credentials after an exchange failure.
	ProviderRefreshErrorUseExistingToken
)

// Lock-result actions determine whether token sources poll the cache or continue
// with their captured credentials when refresh acquisition is reported unavailable.
//
//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	// ProviderLockHeldUseExistingToken continues with captured credentials after unavailable acquisition.
	ProviderLockHeldUseExistingToken ProviderLockHeldAction = iota
	// ProviderLockHeldWaitForCache polls for an access token published during contention.
	ProviderLockHeldWaitForCache
)

// ProviderRefreshErrorAction selects how token sources handle an unsuccessful
// refresh exchange.
type ProviderRefreshErrorAction int

// ProviderLockHeldAction selects how token sources handle an unavailable
// refresh-lock result.
type ProviderLockHeldAction int

// ProviderRefreshPolicy supplies a token source's error action, contention
// action and recovery cache lifetime.
type ProviderRefreshPolicy struct {
	// OnRefreshError selects exchange-error handling. Its zero value requests
	// error propagation when the token source consults this policy.
	OnRefreshError ProviderRefreshErrorAction
	// OnLockHeld selects contention handling. Its zero value permits captured
	// credentials; cache polling requires ProviderLockHeldWaitForCache.
	OnLockHeld ProviderLockHeldAction
	// FailureTTL limits a recovered token's cache lifetime. Claude and OpenAI
	// choose one minute when recovery reaches caching with a nonpositive value.
	FailureTTL time.Duration
}

// ProviderRefreshPlatformPolicy binds Grok request eligibility and
// identity-scoped failure classification for the shared refresh coordinator.
// Missing refresh tokens and configuration or containment errors retain their
// specific error forms for that coordinator's callers.
func ProviderRefreshPlatformPolicy() RefreshPlatformPolicy {
	return RefreshPlatformPolicy{
		Eligibility: GrokOAuthRequestProviderEligibilityError,
		MissingRefreshToken: func() error {
			return ErrGrokOAuthRefreshTokenMissing
		},
		SnapshotError: WithGrokCredentialFailureSnapshot,
		ConfigurationError: func(err error) error {
			return &ProviderConfigurationRefreshError{Cause: err}
		},
		ContainmentError: func(err error) error {
			return &ProviderCycleContainmentRefreshError{Cause: err}
		},
	}
}

// ClaudeProviderRefreshPolicy returns stored-token recovery, cache polling and
// a one-minute failure cache lifetime.
func ClaudeProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorUseExistingToken,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     time.Minute,
	}
}

// OpenAIProviderRefreshPolicy returns stored-token recovery, cache polling and
// a one-minute failure cache lifetime.
func OpenAIProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorUseExistingToken,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     time.Minute,
	}
}

// GeminiProviderRefreshPolicy returns exchange-error propagation and
// existing-token use after an unavailable lock result.
func GeminiProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldUseExistingToken,
		FailureTTL:     0,
	}
}

// AntigravityProviderRefreshPolicy returns exchange-error propagation and
// existing-token use after an unavailable lock result.
func AntigravityProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldUseExistingToken,
		FailureTTL:     0,
	}
}

// GrokProviderRefreshPolicy returns exchange-error propagation with cache
// polling after an unavailable refresh-lock result.
func GrokProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     0,
	}
}
