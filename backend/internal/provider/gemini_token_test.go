package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGeminiTokenCacheKey supplies populated, padded, empty, absent, and nil
// project credentials. It checks that project keys are trimmed and each
// missing project uses the configured row-ID namespace.
func TestGeminiTokenCacheKey(t *testing.T) {
	tests := []struct {
		name     string  // cache identity case name
		provider *Record // input provider credential record
		expected string  // expected complete cache key
	}{
		{
			name: "with_project_id",
			provider: &Record{
				ID: 100,
				Credentials: map[string]any{
					"project_id": "my-project-123",
				},
			},
			expected: "gemini:my-project-123",
		},
		{
			name: "project_id_with_whitespace",
			provider: &Record{
				ID: 101,
				Credentials: map[string]any{
					"project_id": "  project-with-spaces  ",
				},
			},
			expected: "gemini:project-with-spaces",
		},
		{
			name: "empty_project_id_fallback_to_provider_id",
			provider: &Record{
				ID: 102,
				Credentials: map[string]any{
					"project_id": "",
				},
			},
			expected: "gemini:provider:102",
		},
		{
			name: "whitespace_only_project_id_fallback_to_provider_id",
			provider: &Record{
				ID: 103,
				Credentials: map[string]any{
					"project_id": "   ",
				},
			},
			expected: "gemini:provider:103",
		},
		{
			name: "no_project_id_key_fallback_to_provider_id",
			provider: &Record{
				ID:          104,
				Credentials: map[string]any{},
			},
			expected: "gemini:provider:104",
		},
		{
			name: "nil_credentials_fallback_to_provider_id",
			provider: &Record{
				ID:          105,
				Credentials: nil,
			},
			expected: "gemini:provider:105",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GeminiOAuthTokenCacheKey(tt.provider)
			require.Equal(t, tt.expected, result)
		})
	}
}
