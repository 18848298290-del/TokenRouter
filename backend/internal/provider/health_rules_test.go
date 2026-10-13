package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Temporary-pause keywords match a lowercased response while retaining the
// selected keyword's trimmed original spelling. Table cases cover absent inputs
// and keyword selection, then verify byte truncation followed by whitespace
// trimming for the retained diagnostic text.

// TestMatchTempUnschedKeyword supplies lowercased bodies with ordered, blank,
// absent, and uppercase keywords. Results retain the matched keyword's spelling
// and return empty when these fixtures contain no usable match.
func TestMatchTempUnschedKeyword(t *testing.T) {
	tests := []struct {
		name     string   // named table scenario
		body     string   // already lowercased response
		keywords []string // ordered keyword candidates
		want     string   // expected retained spelling
	}{
		{
			name:     "match_first",
			body:     "server is overloaded",
			keywords: []string{"overloaded", "capacity"},
			want:     "overloaded",
		},
		{
			name:     "match_second",
			body:     "no capacity available",
			keywords: []string{"overloaded", "capacity"},
			want:     "capacity",
		},
		{
			name:     "no_match",
			body:     "internal error",
			keywords: []string{"overloaded", "capacity"},
			want:     "",
		},
		{
			name:     "empty_body",
			body:     "",
			keywords: []string{"overloaded"},
			want:     "",
		},
		{
			name:     "empty_keywords",
			body:     "server is overloaded",
			keywords: []string{},
			want:     "",
		},
		{
			name:     "whitespace_keyword",
			body:     "server is overloaded",
			keywords: []string{"  ", "overloaded"},
			want:     "overloaded",
		},
		{
			// The response fixture is already lowercase; uppercase keywords exercise
			// normalization while requiring the returned match to retain its configured
			// spelling.
			name:     "case_insensitive_body_lowered",
			body:     "server is overloaded",
			keywords: []string{"OVERLOADED"},
			want:     "OVERLOADED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchTempUnschedKeyword(tt.body, tt.keywords)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestTruncateTempUnschedMessage checks empty input, zero limits, whitespace
// trimming, and a byte cut ending in a space. Expected text reflects truncation
// before trimming the retained diagnostic.
func TestTruncateTempUnschedMessage(t *testing.T) {
	tests := []struct {
		name     string // named table scenario
		body     []byte // source diagnostic bytes
		maxBytes int    // retained byte limit
		want     string // expected trimmed diagnostic
	}{
		{
			name:     "short_message",
			body:     []byte("short"),
			maxBytes: 100,
			want:     "short",
		},
		{
			// The twentieth byte is a space after long. This fixture distinguishes
			// trimming the truncated prefix from retaining the full diagnostic or its
			// trailing blank.
			name:     "truncate_long_message",
			body:     []byte("this is a very long message that needs to be truncated"),
			maxBytes: 20,
			want:     "this is a very long",
		},
		{
			name:     "empty_body",
			body:     []byte{},
			maxBytes: 100,
			want:     "",
		},
		{
			name:     "zero_max_bytes",
			body:     []byte("test"),
			maxBytes: 0,
			want:     "",
		},
		{
			name:     "whitespace_trimmed",
			body:     []byte("  test  "),
			maxBytes: 100,
			want:     "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateTempUnschedMessage(tt.body, tt.maxBytes)
			require.Equal(t, tt.want, got)
		})
	}
}
