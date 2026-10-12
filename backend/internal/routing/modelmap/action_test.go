package modelmap

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAntigravityGatewayService_IsModelSupported checks exact names, wildcard prefixes, and unknown models.
func TestAntigravityGatewayService_IsModelSupported(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		// Exact matches.
		{"直接支持 - claude-fable-5-1", "claude-fable-5-1", true},
		{"直接支持 - claude-fable-5", "claude-fable-5", true},
		{"直接支持 - claude-sonnet-4-5", "claude-sonnet-4-5", true},
		{"直接支持 - gemini-3-flash", "gemini-3-flash", true},

		// Configured prefix matches.
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
