package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A cached OAuth token may use a project identity or a provider-ID identity,
// and both can survive discovery of the project. Deletion fixtures record
// adapter calls to verify key selection and deduplication across the tested
// platforms. Injected single-key failures also pin the nil invalidation result.

// geminiTokenCacheStub records deletion identities and supplies one configured
// cleanup error.
type geminiTokenCacheStub struct {
	// deletedKeys is appended for every deletion attempt, including attempts
	// returning deleteErr. The fixture's tests access this slice serially.
	deletedKeys []string
	deleteErr   error // injected token deletion error
}

// GetAccessToken supplies an empty successful result for the unused read side
// of AccessTokenCache. The invalidation tests observe DeleteAccessToken calls,
// so this method stores no token state.
func (s *geminiTokenCacheStub) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	return "", nil
}

// SetAccessToken acknowledges cache writes in the deletion fixture. Values and
// ttl are discarded because these tests record deletion identities and inject
// failures through DeleteAccessToken.
func (s *geminiTokenCacheStub) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	return nil
}

// DeleteAccessToken appends the requested key before returning the configured
// error, including failed cleanup attempts. The fixture is used serially;
// callers synchronize access if they introduce concurrent invalidation.
func (s *geminiTokenCacheStub) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	s.deletedKeys = append(s.deletedKeys, cacheKey)
	return s.deleteErr
}

// AcquireRefreshLock reports successful logical acquisition for the unused
// lock side of this deletion fixture. It records no lease, so later
// ReleaseRefreshLock has no state to remove.
func (s *geminiTokenCacheStub) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error) {
	return true, nil
}

// ReleaseRefreshLock acknowledges release for this fixture's unrecorded
// logical acquisitions. It returns nil because deletion tests inject cleanup
// failures through DeleteAccessToken and keep lock behavior independent of
// those assertions.
func (s *geminiTokenCacheStub) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	return nil
}

// TestCompositeTokenCacheInvalidator_Gemini supplies an OAuth record with a
// discovered project ID. Deletion calls cover the selected project identity
// and the earlier provider-ID identity in order.
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

// TestCompositeTokenCacheInvalidator_GeminiWithoutProjectID supplies an OAuth
// record whose project identity is absent. Both selected forms resolve to one
// provider key, and the fixture records one deletion invocation.
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

// TestCompositeTokenCacheInvalidator_Antigravity supplies an OAuth record with
// an Antigravity project ID. The fixture records cleanup of the project
// identity followed by the provider-ID identity.
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

// TestCompositeTokenCacheInvalidator_AntigravityWithoutProjectID supplies an
// OAuth record with no project identity. Deduplication leaves one provider-key
// deletion even though both identity forms select it.
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

// TestCompositeTokenCacheInvalidator_OpenAI supplies an OAuth record with
// stored OpenAI credentials. The fixture records the single provider-ID key
// used by the OpenAI token source.
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

// TestCompositeTokenCacheInvalidator_Claude supplies an OAuth record on the
// Anthropic platform. The fixture records the Claude-prefixed provider-ID key
// used by that token source.
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

// TestCompositeTokenCacheInvalidator_SkipNonOAuth supplies API-key records and
// an Anthropic setup-token record. Each call returns success with an empty
// deletion list, protecting the tested provider-type eligibility rules.
func TestCompositeTokenCacheInvalidator_SkipNonOAuth(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	tests := []struct {
		name     string  // provider invalidation case label
		provider *Record // snapshot selecting cleanup identities
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

// TestCompositeTokenCacheInvalidator_SkipUnsupportedPlatform supplies an OAuth
// record on an unknown platform. The call returns nil and records no deletion
// because no supported token identity is selected.
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

// TestCompositeTokenCacheInvalidator_NilCache supplies an eligible Gemini
// snapshot while omitting the shared cache dependency. Invalidation returns
// success, allowing callers to finish even when cache cleanup is unavailable.
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

// TestCompositeTokenCacheInvalidator_NilProvider invokes an initialized
// invalidator with no provider snapshot. The call returns success and the
// fixture's deletion list remains empty.
func TestCompositeTokenCacheInvalidator_NilProvider(t *testing.T) {
	cache := &geminiTokenCacheStub{}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	err := invalidator.InvalidateToken(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, cache.deletedKeys)
}

// TestCompositeTokenCacheInvalidator_NilInvalidator invokes the method through
// a nil receiver with an otherwise eligible snapshot. The call returns nil,
// protecting optional invalidator wiring.
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

// TestCompositeTokenCacheInvalidator_DeleteError injects deletion errors for
// OpenAI and Anthropic OAuth records. Both invalidation calls return nil,
// pinning the caller-visible result for these two single-key failures.
func TestCompositeTokenCacheInvalidator_DeleteError(t *testing.T) {
	expectedErr := errors.New("redis connection failed")
	cache := &geminiTokenCacheStub{deleteErr: expectedErr}
	invalidator := NewCompositeTokenCacheInvalidator(cache, nil, nil)

	tests := []struct {
		name     string  // provider invalidation case label
		provider *Record // snapshot selecting cleanup identities
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
			// Each case exercises one platform key and asserts the nil result from an
			// injected deletion failure; provider writes are outside this fixture.
			err := invalidator.InvalidateToken(context.Background(), tt.provider)
			require.NoError(t, err)
		})
	}
}

// TestCompositeTokenCacheInvalidator_AllPlatformsIntegration combines Gemini,
// Antigravity, OpenAI and Anthropic records in one serial fixture. The
// recorded adapter calls protect both specific and provider identities for the
// first two platforms and single keys for the others.
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
