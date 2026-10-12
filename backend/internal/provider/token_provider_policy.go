package provider

import (
	"time"
)

const (
	// ProviderRefreshErrorReturn returns the refresh error.
	ProviderRefreshErrorReturn ProviderRefreshErrorAction = iota
	// ProviderRefreshErrorUseExistingToken uses the existing token after refresh failure.
	ProviderRefreshErrorUseExistingToken
)

//nolint:decorder // Separate const blocks keep each iota sequence starting at zero.
const (
	// ProviderLockHeldUseExistingToken uses the existing token while another caller holds the lock.
	ProviderLockHeldUseExistingToken ProviderLockHeldAction = iota
	// ProviderLockHeldWaitForCache waits before reading the token cache again.
	ProviderLockHeldWaitForCache
)

// ProviderRefreshErrorAction selects the response to a token refresh failure.
type ProviderRefreshErrorAction int

// ProviderLockHeldAction selects the response when another caller holds the refresh lock.
type ProviderLockHeldAction int

// ProviderRefreshPolicy holds platform-specific refresh and cache failure settings.
type ProviderRefreshPolicy struct {
	OnRefreshError ProviderRefreshErrorAction
	OnLockHeld     ProviderLockHeldAction
	FailureTTL     time.Duration
}

// ProviderRefreshPlatformPolicy combines Grok refresh eligibility and credential failure snapshots.
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

func ClaudeProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorUseExistingToken,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     time.Minute,
	}
}

func OpenAIProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorUseExistingToken,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     time.Minute,
	}
}

func GeminiProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldUseExistingToken,
		FailureTTL:     0,
	}
}

func AntigravityProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldUseExistingToken,
		FailureTTL:     0,
	}
}

func GrokProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     0,
	}
}
