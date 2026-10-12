package clientmeta

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractVersion(t *testing.T) {
	v := NewClaudeCodeValidator()
	tests := []struct {
		ua   string
		want string
	}{
		{"claude-cli/2.1.22 (darwin; arm64)", "2.1.22"},
		{"claude-cli/1.0.0", "1.0.0"},
		{"Claude-CLI/3.10.5 (linux; x86_64)", "3.10.5"}, // Case-insensitive prefix.
		{"curl/8.0.0", ""},                              // Different client.
		{"", ""},                                        // Empty input.
		{"claude-cli/", ""},                             // Missing version.
		{"claude-cli/2.1.22-beta", "2.1.22"},            // Extract the release components before a suffix.
	}
	for _, tt := range tests {
		got := v.ExtractVersion(tt.ua)
		require.Equal(t, tt.want, got, "ExtractVersion(%q)", tt.ua)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.1.0", "2.1.0", 0},
		{"2.1.1", "2.1.0", 1},
		{"2.0.0", "2.1.0", -1},
		{"3.0.0", "2.99.99", 1},
		{"1.0.0", "2.0.0", -1},
		{"0.0.1", "0.0.0", 1},
		{"", "1.0.0", -1},      // Empty input is treated as zero.
		{"v2.1.0", "2.1.0", 0}, // Leading v is ignored.
	}
	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		require.Equal(t, tt.want, got, "CompareVersions(%q, %q)", tt.a, tt.b)
	}
}
