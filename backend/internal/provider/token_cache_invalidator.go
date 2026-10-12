package provider

import (
	"context"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TokenCacheInvalidator clears credential caches using a provider snapshot.
type TokenCacheInvalidator interface {
	InvalidateToken(ctx context.Context, provider *Record) error
}

// SessionInvalidator invalidates sessions for a provider ID.
type SessionInvalidator interface {
	Invalidate(int64)
}

// CompositeTokenCacheInvalidator deletes platform-specific token keys and logs deletion failures when a
// warning callback is set.
type CompositeTokenCacheInvalidator struct {
	warn               func(string, ...any)
	cache              AccessTokenCache // Platform prefixes share one cache.
	qoderTokenProvider SessionInvalidator
}

func NewCompositeTokenCacheInvalidator(cache AccessTokenCache, qoderTokenProvider SessionInvalidator, warn func(string, ...any),
) *CompositeTokenCacheInvalidator {
	return &CompositeTokenCacheInvalidator{
		warn:               warn,
		cache:              cache,
		qoderTokenProvider: qoderTokenProvider,
	}
}

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
		// Tokens may be cached by provider ID before a project ID is discovered.
		// After discovery the project key is used, so both keys can remain populated.
		// Invalidate both identities.
		keysToDelete = append(keysToDelete, GeminiOAuthTokenCacheKey(provider))
		keysToDelete = append(keysToDelete, "gemini:"+providerIDKey)
	case capability.PlatformAntigravity:
		// Antigravity also uses project and provider identities.
		keysToDelete = append(keysToDelete, AntigravityTokenCacheKey(provider))
		keysToDelete = append(keysToDelete, "ag:"+providerIDKey)
	case capability.PlatformOpenAI:
		keysToDelete = append(keysToDelete, OpenAITokenCacheKey(provider))
	case capability.PlatformGrok:
		keysToDelete = append(keysToDelete, GrokTokenCacheKey(provider))
		keysToDelete = append(keysToDelete, "grok:"+providerIDKey)
	case capability.PlatformAnthropic:
		keysToDelete = append(keysToDelete, ClaudeTokenCacheKey(provider))
	case capability.PlatformQoder:
		if provider.IsQoderCosy() {
			keysToDelete = append(keysToDelete, QoderTokenCacheKey(provider))
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

	// Project lookup may resolve to the provider key; delete each key once.
	seen := make(map[string]bool)
	for _, key := range keysToDelete {
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := c.cache.DeleteAccessToken(ctx, key); err != nil {
			if c.warn != nil {
				c.warn("token_cache_delete_failed", "key", key, "provider_id", provider.ID, "error", err)
			}
		}
	}

	return nil
}
