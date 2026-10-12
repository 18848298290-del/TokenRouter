package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type geminiTokenCacheStub struct {
	deletedKeys []string
	deleteErr   error
}

func (s *geminiTokenCacheStub) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	return "", nil
}

func (s *geminiTokenCacheStub) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	return nil
}

func (s *geminiTokenCacheStub) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	s.deletedKeys = append(s.deletedKeys, cacheKey)
	return s.deleteErr
}

func (s *geminiTokenCacheStub) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error) {
	return true, nil
}

func (s *geminiTokenCacheStub) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	return nil
}

func TestCompositeTokenCacheInvalidator_Gemini(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       10,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"project_id": "project-x",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	// Clear both identities because project discovery can change the cache key
	// after a token has already been cached by provider ID.
	require.Equal(t, []string{"gemini:project-x", "gemini:provider:10"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_GeminiWithoutProjectID(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       10,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "gemini-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	// Missing project IDs resolve to the provider key, which is deleted once.
	require.Equal(t, []string{"gemini:provider:10"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_Antigravity(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       99,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"project_id": "ag-project",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	// Project and provider identities are both invalidated.
	require.Equal(t, []string{"ag:ag-project", "ag:provider:99"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_AntigravityWithoutProjectID(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       99,
		Platform: capability.PlatformAntigravity,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "ag-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	// Missing project IDs resolve to the provider key, which is deleted once.
	require.Equal(t, []string{"ag:provider:99"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_OpenAI(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       500,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "openai-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, []string{"openai:provider:500"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_Claude(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       600,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "claude-token",
		},
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, []string{"claude:provider:600"}, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_SkipNonOAuth(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	tests := []struct {
		name     string
		provider *Record
	}{
		{
			name: "gemini_api_key",
			provider: &Record{
				ID:       1,
				Platform: capability.PlatformGemini,
				Type:     capability.ProviderTypeAPIKey,
			},
		},
		{
			name: "openai_api_key",
			provider: &Record{
				ID:       2,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeAPIKey,
			},
		},
		{
			name: "claude_api_key",
			provider: &Record{
				ID:       3,
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeAPIKey,
			},
		},
		{
			name: "claude_setup_token",
			provider: &Record{
				ID:       4,
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeSetupToken,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache.deletedKeys = nil
			err := invalidator.InvalidateToken(context.Background(), tt.provider)
			require.NoError(t, err)
			require.Empty(t, cache.deletedKeys)
		})
	}
}

func TestCompositeTokenCacheInvalidator_SkipUnsupportedPlatform(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)
	provider := &Record{
		ID:       100,
		Platform: "unknown-platform",
		Type:     capability.ProviderTypeOAuth,
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
	require.Empty(t, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_NilCache(t *testing.T) {
	invalidator := NewCompositeTokenCacheInvalidator(nil, nil, nil)
	provider := &Record{
		ID:       2,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
}

func TestCompositeTokenCacheInvalidator_NilProvider(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	err := invalidator.InvalidateToken(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, cache.deletedKeys)
}

func TestCompositeTokenCacheInvalidator_NilInvalidator(t *testing.T) {
	var invalidator *CompositeTokenCacheInvalidator
	provider := &Record{
		ID:       5,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}

	err := invalidator.InvalidateToken(context.Background(), provider)
	require.NoError(t, err)
}

func TestCompositeTokenCacheInvalidator_DeleteError(t *testing.T) {
	expectedErr := errors.New("redis connection failed")
	cache := &geminiTokenCacheStub{deleteErr: expectedErr}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	tests := []struct {
		name     string
		provider *Record
	}{
		{
			name: "openai_delete_error",
			provider: &Record{
				ID:       700,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
			},
		},
		{
			name: "claude_delete_error",
			provider: &Record{
				ID:       800,
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeOAuth,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Invalidation is best effort: a cache deletion failure returns success
			// so callers can finish their provider update.
			err := invalidator.InvalidateToken(context.Background(), tt.provider)
			require.NoError(t, err)
		})
	}
}

func TestCompositeTokenCacheInvalidator_AllPlatformsIntegration(t *testing.T) {
	// Exercise the key families for these four OAuth platforms.
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	providers := []*Record{
		{ID: 1, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"project_id": "gemini-proj"}},
		{ID: 2, Platform: capability.PlatformAntigravity, Type: capability.ProviderTypeOAuth, Credentials: map[string]any{"project_id": "ag-proj"}},
		{ID: 3, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth},
		{ID: 4, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth},
	}

	// Gemini and Antigravity invalidate both project and provider keys.
	expectedKeys := []string{
		"gemini:gemini-proj",
		"gemini:provider:1",
		"ag:ag-proj",
		"ag:provider:2",
		"openai:provider:3",
		"claude:provider:4",
	}

	for _, acc := range providers {
		err := invalidator.InvalidateToken(context.Background(), acc)
		require.NoError(t, err)
	}

	require.Equal(t, expectedKeys, cache.deletedKeys)
}
