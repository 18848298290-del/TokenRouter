package logredact

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSafeUpstreamURL supplies endpoints with query strings, fragments, both
// suffixes, and blank input. The returned display value must match the expected
// endpoint prefix after trimming and suffix removal.
func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string // subtest display name
		input string // supplied endpoint text
		want  string // expected displayed endpoint
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, SafeUpstreamURL(tt.input))
		})
	}
}
