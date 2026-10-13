package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGrokTokenCacheKey supplies ordinary, metadata-bearing, zero-ID, and nil
// provider records. It asserts keys depend on decimal provider identity and the
// nil case shares the zero-ID sentinel.
func TestGrokTokenCacheKey(t *testing.T) {
	tests := []struct {
		name     string  // table case display name
		provider *Record // supplied provider identity fixture
		expected string  // expected provider cache key
	}{
		{
			name: "basic_provider",
			provider: &Record{
				ID: 350,
			},
			expected: "grok:provider:350",
		},
		{
			name: "provider_with_email_uses_provider_id",
			provider: &Record{
				ID: 351,
				Credentials: map[string]any{
					"email": "same-user@example.com",
				},
			},
			expected: "grok:provider:351",
		},
		{
			name: "provider_id_zero",
			provider: &Record{
				ID: 0,
			},
			expected: "grok:provider:0",
		},
		{
			name:     "nil_provider",
			provider: nil,
			expected: "grok:provider:0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GrokTokenCacheKey(tt.provider)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestGrokTokenCacheKeySeparatesProvidersWithSameEmail assigns the same email
// to two distinct stored provider IDs. It asserts their cache keys differ so
// email metadata cannot merge those provider identities.
func TestGrokTokenCacheKeySeparatesProvidersWithSameEmail(t *testing.T) {
	first := &Record{
		ID: 351,
		Credentials: map[string]any{
			"email": "same-user@example.com",
		},
	}
	second := &Record{
		ID: 352,
		Credentials: map[string]any{
			"email": "same-user@example.com",
		},
	}

	require.NotEqual(t, GrokTokenCacheKey(first), GrokTokenCacheKey(second))
}
