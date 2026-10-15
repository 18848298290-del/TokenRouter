package provider

import (
	"context"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// token_cache_invalidator.go invalidates platform token cache identities and
// Qoder credential sessions after provider changes.
// A token cached before project or account discovery can remain under a
// provider-ID key after the current snapshot selects a more specific identity.
// Gemini, Antigravity and Grok invalidation therefore covers both the
// snapshot's selected key and the provider key, while OpenAI and Anthropic use
// their single provider identity.
//
// Key selection follows the supplied snapshot; duplicate identities produce
// one DeleteAccessToken invocation each. Qoder COSY also invalidates its
// in-memory session before shared-cache deletion, including when the shared
// cache is absent. Cache deletion failures can be logged and leave remaining
// keys eligible for cleanup. The operation returns success after those
// failures, so provider updates can finish with cache cleanup still
// incomplete.

// TokenCacheInvalidator removes access-token entries associated with a
// supplied provider snapshot.
type TokenCacheInvalidator interface {
	// InvalidateToken clears the token state represented by provider using the
	// supplied operation context. Callers keep the snapshot stable during the
	// call and handle the implementation's returned cleanup error.
	InvalidateToken(ctx context.Context, provider *Record) error
}

// SessionInvalidator discards runtime session state identified by a provider
// ID.
type SessionInvalidator interface {
	// Invalidate discards runtime session state associated with the supplied provider ID.
	Invalidate(int64)
}

// CompositeTokenCacheInvalidator selects platform token keys and coordinates
// Qoder COSY session invalidation.
type CompositeTokenCacheInvalidator struct {
	warn func(string, ...any) // optional deletion failure observer
	// cache is borrowed across all platform prefixes. A nil cache skips Redis
	// deletion after any selected Qoder runtime session has been invalidated.
	cache AccessTokenCache
	// qoderTokenProvider is optional and is called for COSY snapshots before
	// shared token cleanup, even when the shared cache dependency is absent.
	qoderTokenProvider SessionInvalidator
}

// NewCompositeTokenCacheInvalidator binds the shared token cache, optional
// Qoder session invalidator and warning callback. Dependencies are borrowed,
// and callers may omit any of them to disable the corresponding cleanup or
// logging.
func NewCompositeTokenCacheInvalidator(cache AccessTokenCache, qoderTokenProvider SessionInvalidator, warn func(string, ...any),
) *CompositeTokenCacheInvalidator {
	return &CompositeTokenCacheInvalidator{
		warn:               warn,
		cache:              cache,
		qoderTokenProvider: qoderTokenProvider,
	}
}

// InvalidateToken attempts platform-specific cleanup for OAuth snapshots and
// Qoder COSY records. Nil receivers or records, unsupported platforms and
// other provider types return success without selecting token keys.
//
// Project or account identities can coexist with a provider-ID identity, so
// both are selected and duplicate keys are collapsed. The snapshot determines
// these identities; a formerly used project key absent from that snapshot is
// not discovered here. Qoder session invalidation precedes shared-cache
// cleanup. Each selected key receives one adapter invocation, which Redis may
// retry internally; deletion failures are optionally logged, remaining keys
// are attempted, and the method returns nil.
func (c *CompositeTokenCacheInvalidator) InvalidateToken(ctx context.Context, provider *Record) error {
	if c == nil || provider == nil {
		return nil
	}
	if provider.Type != capability.ProviderTypeOAuth && !provider.IsQoderCosy() {
		return nil
	}

	var keysToDelete []string
	providerIDKey := "provider:" + strconv.FormatInt(provider.ID, 10)

	switch provider.Platform {
	case capability.PlatformGemini:
		// Project discovery changes Gemini's selected identity, while the earlier
		// provider entry can survive; clear both keys represented by this snapshot.
		keysToDelete = append(keysToDelete, GeminiOAuthTokenCacheKey(provider))
		keysToDelete = append(keysToDelete, "gemini:"+providerIDKey)
	case capability.PlatformAntigravity:
		// Antigravity project discovery can leave the earlier provider entry populated;
		// clear both identities represented by the current snapshot.
		keysToDelete = append(keysToDelete, AntigravityTokenCacheKey(provider))
		keysToDelete = append(keysToDelete, "ag:"+providerIDKey)
	case capability.PlatformOpenAI:
		keysToDelete = append(keysToDelete, OpenAITokenCacheKey(provider))
	case capability.PlatformGrok:
		// Grok can select an account identity after provider-key caching, so clear
		// both selected and provider identities to remove either represented entry.
		keysToDelete = append(keysToDelete, GrokTokenCacheKey(provider))
		keysToDelete = append(keysToDelete, "grok:"+providerIDKey)
	case capability.PlatformAnthropic:
		keysToDelete = append(keysToDelete, ClaudeTokenCacheKey(provider))
	case capability.PlatformQoder:
		if provider.IsQoderCosy() {
			keysToDelete = append(keysToDelete, QoderTokenCacheKey(provider))
			// COSY sessions retain credentials outside the shared token cache; discard
			// that runtime state before attempting the separately stored token deletion.
			if c.qoderTokenProvider != nil {
				c.qoderTokenProvider.Invalidate(provider.ID)
			}
		}
	default:
		return nil
	}

	if c.cache == nil {
		return nil
	}

	// A missing specific identity resolves to the provider key again; deduplicate
	// adapter invocations so that this overlap does not cause repeated cleanup.
	seen := make(map[string]bool)
	for _, key := range keysToDelete {
		if seen[key] {
			continue
		}
		seen[key] = true
		// A deletion failure leaves this entry uncertain, but other selected entries
		// still need cleanup and callers finish the provider update with nil.
		if err := c.cache.DeleteAccessToken(ctx, key); err != nil {
			if c.warn != nil {
				c.warn("token_cache_delete_failed", "key", key, "provider_id", provider.ID, "error", err)
			}
		}
	}

	return nil
}
