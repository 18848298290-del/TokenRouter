package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestAntigravityTokenProvider_GetAccessToken_Guards(t *testing.T) {
	tokenSource := &AntigravityTokenSource{}

	t.Run("nil provider", func(t *testing.T) {
		token, err := tokenSource.GetAccessToken(context.Background(), nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "provider is nil")
		require.Empty(t, token)
	})

	t.Run("non-antigravity platform", func(t *testing.T) {
		provider := &Record{
			Platform: capability.PlatformAnthropic,
			Type:     capability.ProviderTypeOAuth,
		}
		token, err := tokenSource.GetAccessToken(context.Background(), provider)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not an antigravity provider")
		require.Empty(t, token)
	})

	// Static keys are not accepted as OAuth access tokens.
	for _, kind := range []string{capability.ProviderTypeAPIKey, capability.ProviderTypeUpstream} {
		t.Run(kind, func(t *testing.T) {
			provider := &Record{
				Platform:    capability.PlatformAntigravity,
				Type:        kind,
				Credentials: map[string]any{"api_key": "fixture-key"},
			}
			token, err := tokenSource.GetAccessToken(context.Background(), provider)
			require.Error(t, err)
			require.Contains(t, err.Error(), "not an antigravity oauth provider")
			require.Empty(t, token)
		})
	}
}

func TestAntigravityTokenCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		expected string
	}{
		{
			name: "with_project_id",
			provider: &Record{
				ID: 200,
				Credentials: map[string]any{
					"project_id": "ag-project-456",
				},
			},
			expected: "ag:ag-project-456",
		},
		{
			name: "project_id_with_whitespace",
			provider: &Record{
				ID: 201,
				Credentials: map[string]any{
					"project_id": "  ag-project-spaces  ",
				},
			},
			expected: "ag:ag-project-spaces",
		},
		{
			name: "empty_project_id_fallback_to_provider_id",
			provider: &Record{
				ID: 202,
				Credentials: map[string]any{
					"project_id": "",
				},
			},
			expected: "ag:provider:202",
		},
		{
			name: "whitespace_only_project_id_fallback_to_provider_id",
			provider: &Record{
				ID: 203,
				Credentials: map[string]any{
					"project_id": "   ",
				},
			},
			expected: "ag:provider:203",
		},
		{
			name: "no_project_id_key_fallback_to_provider_id",
			provider: &Record{
				ID:          204,
				Credentials: map[string]any{},
			},
			expected: "ag:provider:204",
		},
		{
			name: "nil_credentials_fallback_to_provider_id",
			provider: &Record{
				ID:          205,
				Credentials: nil,
			},
			expected: "ag:provider:205",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AntigravityTokenCacheKey(tt.provider)
			require.Equal(t, tt.expected, result)
		})
	}
}
