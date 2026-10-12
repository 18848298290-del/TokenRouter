package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchTempUnschedKeyword(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		keywords []string
		want     string
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
			// MatchTempUnschedKeyword requires an already-lowercased body and normalizes keywords itself.
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

func TestTruncateTempUnschedMessage(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		maxBytes int
		want     string
	}{
		{
			name:     "short_message",
			body:     []byte("short"),
			maxBytes: 100,
			want:     "short",
		},
		{
			// Truncation is followed by TrimSpace, removing the trailing space at the cut.
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
