package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestManagedRecoveryFencePreservesNewRuntimeBlock checks that recovery leaves newer blocks intact and can
// clear the current generation.
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

// TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock checks that the default cooldown cannot shorten a longer block.
func TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock(t *testing.T) {
	svc := NewRuntimeBlockState(time.Now)
	provider := &Record{ID: 46, Platform: PlatformOpenAI, Type: ProviderTypeOAuth}
	longUntil := time.Now().Add(10 * time.Minute)

	svc.Block(provider.ID, longUntil, "oauth_401")
	svc.Block(provider.ID, time.Time{}, "upstream_disable")

	value, ok := svc.until.Load(provider.ID)
	require.True(t, ok)
	actualUntil, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, longUntil, actualUntil, time.Second)
}

func TestOpenAIRuntimeBlock_ClearProviderSchedulingBlock(t *testing.T) {
	svc := NewRuntimeBlockState(time.Now)
	provider := &Record{ID: 47, Platform: PlatformOpenAI, Type: ProviderTypeOAuth}

	svc.Block(provider.ID, time.Now().Add(time.Minute), "429")
	require.True(t, svc.Blocked(provider.ID, func() string { return RefreshCredentialIdentity(provider) }))

	svc.ClearProviderSchedulingBlock(provider.ID)
	require.False(t, svc.Blocked(provider.ID, func() string { return RefreshCredentialIdentity(provider) }))
}

// TestRefreshRuntimeBlockIsCredentialScoped checks that a late failure notice for old credentials cannot block
// replacement credentials.
func TestRefreshRuntimeBlockIsCredentialScoped(t *testing.T) {
	gateway := NewRuntimeBlockState(time.Now)
	old := &Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"access_token": "old-fixture"}}
	fresh := *old
	fresh.Credentials = map[string]any{"access_token": "new-fixture"}
	PrepareRefreshFailureNotice(gateway, old)(time.Now().Add(time.Minute), "token_refresh_non_retryable")
	require.True(t, gateway.Blocked(old.ID, func() string { return RefreshCredentialIdentity(old) }))
	require.False(t, gateway.Blocked(fresh.ID, func() string { return RefreshCredentialIdentity(&fresh) }), "旧凭据失败通知阻断了新凭据")
}

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
	// Provider-wide capacity and quota blocks cover every credential identity.
	gateway.BlockProviderScheduling(c, time.Now().Add(time.Hour), "429")
	require.True(t, blocked(c))
	gateway.ClearProviderSchedulingBlock(1)
	publishA(notice(a))
	publishB(notice(b))
	require.False(t, blocked(a))
	require.False(t, blocked(b))
	gateway.PrepareRefreshFailure(1)(notice(c))
	require.True(t, blocked(c))
	// Rolling back a provisional Grok block leaves independent credential-scoped failure blocks intact.
	publishAfterProbe := gateway.PrepareRefreshFailure(1)
	release := gateway.BlockRollback(1, time.Now().Add(time.Minute), "credential_probe")
	release()
	publishAfterProbe(notice(value("d")))
	require.True(t, blocked(value("d")))
	require.True(t, blocked(c))
	require.False(t, blocked(a))
}
