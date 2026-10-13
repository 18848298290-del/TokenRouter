package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A Spark shadow follows its parent's proxy and credential health while
// retaining an independent scheduling and quota window. These tests propagate
// proxy replacement and clearing, then compare parent identity, status,
// expiration, and temporary suspension with the parent's separate scheduling
// controls.

// TestPropagateProviderProxyToShadows starts parent and shadow records on the
// same proxy in the shared-record fixture. Passing a replacement selection and
// nil to propagation changes the stored shadow proxy to match each input.
func TestPropagateProviderProxyToShadows(t *testing.T) {
	ctx := context.Background()
	repo := newCRSShadowStore()

	oldProxy := int64(11)
	mother := &Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ProxyID: &oldProxy}
	require.NoError(t, repo.Create(ctx, mother))
	parentID := mother.ID

	shadow := &Record{
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   QuotaDimensionSpark,
		ProxyID:          &oldProxy,
	}
	require.NoError(t, repo.Create(ctx, shadow))

	newProxy := int64(22)
	require.NoError(t, PropagateProviderProxyToShadows(ctx, repo, parentID, &newProxy))

	got, err := repo.GetByID(ctx, shadow.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ProxyID)
	require.Equal(t, newProxy, *got.ProxyID, "shadow proxy must follow the parent's new proxy")

	// A nil selection clears the shadow's stored proxy pointer.
	require.NoError(t, PropagateProviderProxyToShadows(ctx, repo, parentID, nil))
	got, err = repo.GetByID(ctx, shadow.ID)
	require.NoError(t, err)
	require.Nil(t, got.ProxyID, "clearing parent proxy must clear the shadow proxy too")
}

// TestParentHealthyForShadow varies the shared parent's identity and health
// conditions. Missing, inactive, expired, suspended, or non-OAuth parents block
// the shadow, while parent scheduling switches, global rate limits, and
// overload windows stay independent.
func TestParentHealthyForShadow(t *testing.T) {
	pid := int64(100)

	// OpenAI OAuth fixtures isolate each parent health condition.
	healthyParent := &Record{
		ID:          100,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
	}
	unhealthyParent := &Record{
		ID:          100,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Status:      StatusError,
		Schedulable: true, // Error status overrides the schedulable flag.
	}
	shadow := &Record{
		ID:               200,
		ParentProviderID: &pid,
		QuotaDimension:   QuotaDimensionSpark,
		Platform:         capability.PlatformOpenAI,
		Status:           billing.StatusActive,
		Schedulable:      true,
	}
	normalProvider := &Record{
		ID:          300,
		Platform:    capability.PlatformOpenAI,
		Status:      billing.StatusActive,
		Schedulable: true,
	}

	t.Run("shadow_of_healthy_parent_is_healthy", func(t *testing.T) {
		lookup := func(id int64) *Record {
			if id == healthyParent.ID {
				return healthyParent
			}
			return nil
		}
		require.True(t, ParentHealthyForShadow(shadow, lookup))
	})

	t.Run("shadow_of_unhealthy_parent_is_not_healthy", func(t *testing.T) {
		// Error status makes both IsActive and IsSchedulable return false.
		require.False(t, unhealthyParent.IsSchedulable(), "precondition: unhealthy parent must not be schedulable")
		lookup := func(id int64) *Record {
			if id == unhealthyParent.ID {
				return unhealthyParent
			}
			return nil
		}
		require.False(t, ParentHealthyForShadow(shadow, lookup))
	})

	t.Run("shadow_parent_not_found_is_not_healthy", func(t *testing.T) {
		lookup := func(_ int64) *Record { return nil }
		require.False(t, ParentHealthyForShadow(shadow, lookup))
	})

	t.Run("normal_provider_always_healthy", func(t *testing.T) {
		// Ordinary providers need no parent lookup.
		calledLookup := false
		lookup := func(_ int64) *Record {
			calledLookup = true
			return nil
		}
		require.True(t, ParentHealthyForShadow(normalProvider, lookup))
		require.False(t, calledLookup, "lookup must not be called for non-shadow providers")
	})

	t.Run("nil_provider_always_healthy", func(t *testing.T) {
		lookup := func(_ int64) *Record { return nil }
		require.True(t, ParentHealthyForShadow(nil, lookup))
	})

	t.Run("manual_schedulable_false_parent_does_not_block_shadow", func(t *testing.T) {
		// A shadow uses its own scheduling switch when its parent is manually paused.
		// Active, unexpired parent credentials remain eligible for the shadow.
		manualPausedParent := &Record{
			ID:          100,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeOAuth,
			Status:      billing.StatusActive,
			Schedulable: false,
		}
		require.False(t, manualPausedParent.IsSchedulable(), "precondition: 母提供商被手动暂停不可调度")
		lookup := func(id int64) *Record {
			if id == manualPausedParent.ID {
				return manualPausedParent
			}
			return nil
		}
		require.True(t, ParentHealthyForShadow(shadow, lookup),
			"母提供商手动暂停不应连坐影子(凭据仍可用)")
	})

	t.Run("global_rate_limited_parent_does_not_block_shadow", func(t *testing.T) {
		// Spark has its own quota window, so a global rate limit on the parent
		// does not block the shadow.
		resetAt := time.Now().Add(1 * time.Hour)
		rateLimitedParent := &Record{
			ID:               100,
			Platform:         capability.PlatformOpenAI,
			Type:             capability.ProviderTypeOAuth,
			Status:           billing.StatusActive,
			Schedulable:      true,
			RateLimitResetAt: &resetAt,
		}
		require.False(t, rateLimitedParent.IsSchedulable(), "precondition: global 限流母提供商自身不可调度")
		lookup := func(id int64) *Record {
			if id == rateLimitedParent.ID {
				return rateLimitedParent
			}
			return nil
		}
		require.True(t, ParentHealthyForShadow(shadow, lookup),
			"母提供商 global 限流不应连坐 spark 影子")
	})

	t.Run("overloaded_parent_does_not_block_shadow", func(t *testing.T) {
		// Global overload backoff does not block the independent Spark shadow.
		until := time.Now().Add(30 * time.Minute)
		overloadedParent := &Record{
			ID:            100,
			Platform:      capability.PlatformOpenAI,
			Type:          capability.ProviderTypeOAuth,
			Status:        billing.StatusActive,
			Schedulable:   true,
			OverloadUntil: &until,
		}
		require.False(t, overloadedParent.IsSchedulable(), "precondition: 过载母提供商自身不可调度")
		lookup := func(id int64) *Record {
			if id == overloadedParent.ID {
				return overloadedParent
			}
			return nil
		}
		require.True(t, ParentHealthyForShadow(shadow, lookup),
			"母提供商过载退避不应连坐 spark 影子")
	})

	t.Run("temp_unschedulable_parent_blocks_shadow", func(t *testing.T) {
		// Temporary suspension blocks shadows that share the parent token and proxy.
		// This differs from the parent's global quota window.
		until := time.Now().Add(15 * time.Minute)
		tempUnschedParent := &Record{
			ID:                     100,
			Platform:               capability.PlatformOpenAI,
			Type:                   capability.ProviderTypeOAuth,
			Status:                 billing.StatusActive,
			Schedulable:            true,
			TempUnschedulableUntil: &until,
		}
		lookup := func(id int64) *Record {
			if id == tempUnschedParent.ID {
				return tempUnschedParent
			}
			return nil
		}
		require.False(t, ParentHealthyForShadow(shadow, lookup),
			"母提供商 TempUnschedulableUntil(凭据/传输坏死)冷却期内应挡住影子")
	})

	t.Run("expired_parent_credentials_block_shadow", func(t *testing.T) {
		// Expiry blocks the shadow when AutoPauseOnExpired is enabled.
		expiredAt := time.Now().Add(-1 * time.Hour)
		expiredParent := &Record{
			ID:                 100,
			Platform:           capability.PlatformOpenAI,
			Type:               capability.ProviderTypeOAuth,
			Status:             billing.StatusActive,
			Schedulable:        true,
			AutoPauseOnExpired: true,
			ExpiresAt:          &expiredAt,
		}
		lookup := func(id int64) *Record {
			if id == expiredParent.ID {
				return expiredParent
			}
			return nil
		}
		require.False(t, ParentHealthyForShadow(shadow, lookup),
			"母提供商凭据过期时影子应被挡(透传 token 不可用)")
	})

	t.Run("non_oauth_parent_blocks_shadow", func(t *testing.T) {
		// Shadows require an OpenAI OAuth parent even when another provider type
		// is active and unexpired.
		apikeyParent := &Record{
			ID:          100,
			Platform:    capability.PlatformOpenAI,
			Type:        capability.ProviderTypeAPIKey,
			Status:      billing.StatusActive,
			Schedulable: true,
		}
		lookup := func(id int64) *Record {
			if id == apikeyParent.ID {
				return apikeyParent
			}
			return nil
		}
		require.False(t, ParentHealthyForShadow(shadow, lookup),
			"母提供商非 OpenAI OAuth 时影子应被挡(fail-closed)")
	})
}
