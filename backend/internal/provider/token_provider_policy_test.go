package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Refresh policies separate the result of an exchange failure from the
// response to lock contention. These assertions pin the configured actions and
// failure cache duration for four platforms, so changing a factory cannot
// silently change its token source's recovery settings.

// TestClaudeProviderRefreshPolicy reads Claude's request refresh policy from
// its factory. The assertions pin stored-token recovery, cache polling after
// contention and the one-minute failure cache lifetime.
func TestClaudeProviderRefreshPolicy(t *testing.T) {
	p := ClaudeProviderRefreshPolicy()
	require.Equal(t, ProviderRefreshErrorUseExistingToken, p.OnRefreshError)
	require.Equal(t, ProviderLockHeldWaitForCache, p.OnLockHeld)
	require.Equal(t, time.Minute, p.FailureTTL)
}

// TestOpenAIProviderRefreshPolicy reads OpenAI's request refresh policy from
// its factory. The assertions pin stored-token recovery, cache polling after
// contention and the one-minute failure cache lifetime.
func TestOpenAIProviderRefreshPolicy(t *testing.T) {
	p := OpenAIProviderRefreshPolicy()
	require.Equal(t, ProviderRefreshErrorUseExistingToken, p.OnRefreshError)
	require.Equal(t, ProviderLockHeldWaitForCache, p.OnLockHeld)
	require.Equal(t, time.Minute, p.FailureTTL)
}

// TestGeminiProviderRefreshPolicy reads Gemini's request refresh policy from
// its factory. The assertions pin exchange-error propagation, captured-token
// use after contention and the zero failure cache duration.
func TestGeminiProviderRefreshPolicy(t *testing.T) {
	p := GeminiProviderRefreshPolicy()
	require.Equal(t, ProviderRefreshErrorReturn, p.OnRefreshError)
	require.Equal(t, ProviderLockHeldUseExistingToken, p.OnLockHeld)
	require.Equal(t, time.Duration(0), p.FailureTTL)
}

// TestAntigravityProviderRefreshPolicy reads Antigravity's request refresh
// policy from its factory. The assertions pin exchange-error propagation,
// captured-token use after contention and the zero failure cache duration.
func TestAntigravityProviderRefreshPolicy(t *testing.T) {
	p := AntigravityProviderRefreshPolicy()
	require.Equal(t, ProviderRefreshErrorReturn, p.OnRefreshError)
	require.Equal(t, ProviderLockHeldUseExistingToken, p.OnLockHeld)
	require.Equal(t, time.Duration(0), p.FailureTTL)
}
