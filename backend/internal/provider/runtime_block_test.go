package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Runtime recovery uses provider generations to distinguish an observed block
// from a newer declaration. Fixtures also separate provider-wide cooldowns from
// credential identities, checking that clearing invalidates old publishers
// while provisional rollback leaves credential failures available for
// subsequent observations.

// TestManagedRecoveryFencePreservesNewRuntimeBlock captures a fence before a
// later provider declaration. Recovery rejects that stale fence, clears with
// the current one, and rejects the earlier fence again after cleanup.
func TestManagedRecoveryFencePreservesNewRuntimeBlock(t *testing.T) {
	s := NewRuntimeBlockState(time.Now)
	a := &Record{LoadLocation: time.LoadLocation, ID: 72, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	s.BlockProviderScheduling(a, time.Now().Add(time.Minute), "first")
	fence := s.ManagedRecoveryFence(a.ID)
	s.BlockProviderScheduling(a, time.Now().Add(2*time.Minute), "new")
	require.False(t, s.ClearProviderSchedulingBlockIfFence(a.ID, fence))
	require.True(t, s.Blocked(a.ID, func() string { return RefreshCredentialIdentity(a) }))
	require.True(t, s.ClearProviderSchedulingBlockIfFence(a.ID, s.ManagedRecoveryFence(a.ID)))
	require.False(t, s.Blocked(a.ID, func() string { return RefreshCredentialIdentity(a) }))
	require.False(t, s.ClearProviderSchedulingBlockIfFence(a.ID, fence))
}

// TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock declares a ten-minute
// block before a missing-deadline block. The stored deadline remains within one
// second of the longer window after fallback resolution.
func TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock(t *testing.T) {
	svc := NewRuntimeBlockState(time.Now)
	provider := &Record{ID: 46, Platform: PlatformOpenAI, Type: ProviderTypeOAuth}
	// Ten minutes exceeds the two-minute fallback and exposes shortening.
	longUntil := time.Now().Add(10 * time.Minute)

	svc.Block(provider.ID, longUntil, "oauth_401")
	svc.Block(provider.ID, time.Time{}, "upstream_disable")

	value, ok := svc.until.Load(provider.ID)
	require.True(t, ok)
	actualUntil, ok := value.(time.Time)
	require.True(t, ok)
	// Deadline retention is checked with one-second tolerance, so this assertion
	// accepts small timestamp differences.
	require.WithinDuration(t, longUntil, actualUntil, time.Second)
}

// TestOpenAIRuntimeBlock_ClearProviderSchedulingBlock installs a live
// provider-wide cooldown. Blocked returns true before unconditional clearing
// and false afterward for the same provider and credential callback.
func TestOpenAIRuntimeBlock_ClearProviderSchedulingBlock(t *testing.T) {
	svc := NewRuntimeBlockState(time.Now)
	provider := &Record{ID: 47, Platform: PlatformOpenAI, Type: ProviderTypeOAuth}

	svc.Block(provider.ID, time.Now().Add(time.Minute), "429")
	require.True(t, svc.Blocked(provider.ID, func() string { return RefreshCredentialIdentity(provider) }))

	svc.ClearProviderSchedulingBlock(provider.ID)
	require.False(t, svc.Blocked(provider.ID, func() string { return RefreshCredentialIdentity(provider) }))
}

// TestRefreshRuntimeBlockIsCredentialScoped publishes a refresh failure for an
// old token. The old identity is blocked, while a record sharing its provider
// ID with replacement credentials remains unblocked.
func TestRefreshRuntimeBlockIsCredentialScoped(t *testing.T) {
	gateway := NewRuntimeBlockState(time.Now)
	old := &Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"access_token": "old-fixture"}}
	fresh := *old
	fresh.Credentials = map[string]any{"access_token": "new-fixture"}
	PrepareRefreshFailureNotice(gateway, old)(time.Now().Add(time.Minute), "token_refresh_non_retryable")
	require.True(t, gateway.Blocked(old.ID, func() string { return RefreshCredentialIdentity(old) }))
	require.False(t, gateway.Blocked(fresh.ID, func() string { return RefreshCredentialIdentity(&fresh) }), "旧凭据失败通知阻断了新凭据")
}

// TestRefreshRuntimePublicationHonorsClearAndOtherVersions publishes two
// credential failures and a provider-wide block for one ID. Clear rejects both
// old publishers; a fresh publisher works, and provisional rollback leaves
// credential-specific failures and later publication usable.
func TestRefreshRuntimePublicationHonorsClearAndOtherVersions(t *testing.T) {
	gateway := NewRuntimeBlockState(time.Now)
	blocked := func(v *Record) bool {
		return gateway.Blocked(v.ID, func() string { return RefreshCredentialIdentity(v) })
	}
	value := func(token string) *Record {
		return &Record{LoadLocation: time.LoadLocation, ID: 1, Platform: PlatformGrok, Type: ProviderTypeOAuth, Credentials: map[string]any{"access_token": token}}
	}
	a, b, c := value("a"), value("b"), value("c")
	notice := func(v *Record) RefreshFailureNotice {
		n := FailureNotice(v)
		n.Until = time.Now().Add(time.Minute)
		return n
	}
	publishA, publishB := gateway.PrepareRefreshFailure(1), gateway.PrepareRefreshFailure(1)
	publishB(notice(b))
	publishA(notice(a))
	require.True(t, blocked(a))
	require.True(t, blocked(b))
	require.False(t, blocked(c))
	// Identity c has no published failure, so its next blocked result must come
	// from the provider-wide window rather than either credential entry.
	gateway.BlockProviderScheduling(c, time.Now().Add(time.Hour), "429")
	require.True(t, blocked(c))
	// Clear retires publishers captured for a and b. Calling those same closures
	// again tests rejection after recovery even when their notices still have
	// future deadlines.
	gateway.ClearProviderSchedulingBlock(1)
	publishA(notice(a))
	publishB(notice(b))
	require.False(t, blocked(a))
	require.False(t, blocked(b))
	gateway.PrepareRefreshFailure(1)(notice(c))
	require.True(t, blocked(c))
	// Capture another publisher before the provisional provider block is rolled
	// back. Its later d notice remains usable, while the prior c failure also
	// remains active.
	publishAfterProbe := gateway.PrepareRefreshFailure(1)
	release := gateway.BlockRollback(1, time.Now().Add(time.Minute), "credential_probe")
	release()
	publishAfterProbe(notice(value("d")))
	require.True(t, blocked(value("d")))
	require.True(t, blocked(c))
	require.False(t, blocked(a))
}
