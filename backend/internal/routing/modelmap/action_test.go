package modelmap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A broad model-prefix whitelist accepts concrete names even when those names
// have no exact configured entry. The Claude and Gemini fixtures check those
// prefix matches alongside unrelated and empty inputs, keeping the test's
// assertions aligned with its two wildcard patterns.

// TestAntigravityGatewayService_IsModelSupported checks names against claude-*
// and gemini-* whitelist patterns. Known and unknown names under those prefixes
// must match, while unrelated and empty inputs are rejected.
func TestAntigravityGatewayService_IsModelSupported(t *testing.T) {
	tests := []struct {
		name     string // subtest case display name
		model    string // model passed to whitelist
		expected bool   // expected wildcard membership result
	}{
		// Concrete names under the configured Claude and Gemini prefixes.
		{"直接支持 - claude-fable-5-1", "claude-fable-5-1", true},
		{"直接支持 - claude-fable-5", "claude-fable-5", true},
		{"直接支持 - claude-sonnet-4-5", "claude-sonnet-4-5", true},
		{"直接支持 - gemini-3-flash", "gemini-3-flash", true},

		// Additional concrete versions covered by the same configured prefixes.
		{"可映射 - claude-opus-4-8", "claude-opus-4-8", true},
		{"可映射 - claude-opus-4-6", "claude-opus-4-6", true},

		// Claude and Gemini prefix matches.
		{"Gemini前缀", "gemini-unknown", true},
		{"Claude前缀", "claude-unknown", true},

		// Unsupported names.
		{"不支持 - gpt-4", "gpt-4", false},
		{"不支持 - 空字符串", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesAny(tt.model, []string{"claude-*", "gemini-*"})
			require.Equal(t, tt.expected, got)
		})
	}
}
