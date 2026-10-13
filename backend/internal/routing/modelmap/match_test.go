package modelmap

import (
	"testing"
)

// Trailing-star model patterns accept both populated and empty suffixes while
// ordinary text requires exact equality. Mapping fixtures check overlapping
// source selection and unmatched-name retention by calling MatchWildcard
// directly, including exact strings that the matcher accepts.

// TestMatchWildcard compares exact text, trailing-star prefixes, empty inputs,
// and a universal star. Each supplied pattern must produce the expected
// boolean, including acceptance of an empty suffix after its prefix.
func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name     string // subtest case display name
		pattern  string // model pattern under test
		str      string // candidate model string
		expected bool   // expected pattern match result
	}{
		// Exact matches.
		{"exact match", "claude-sonnet-4-5", "claude-sonnet-4-5", true},
		{"exact mismatch", "claude-sonnet-4-5", "claude-opus-4-5", false},

		// Trailing wildcards.
		{"wildcard prefix match", "claude-*", "claude-sonnet-4-5", true},
		{"wildcard prefix match 2", "claude-*", "claude-opus-4-5-thinking", true},
		{"wildcard prefix mismatch", "claude-*", "gemini-3-flash", false},
		{"wildcard partial match", "gemini-3*", "gemini-3-flash", true},
		{"wildcard partial match 2", "gemini-3*", "gemini-3-pro-image", true},
		{"wildcard partial mismatch", "gemini-3*", "gemini-2.5-flash", false},

		// Empty strings and patterns.
		{"empty pattern exact", "", "", true},
		{"empty pattern mismatch", "", "claude", false},
		{"single star", "*", "anything", true},
		{"star at end only", "abc*", "abcdef", true},
		{"star at end empty suffix", "abc*", "abc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Matches(tt.pattern, tt.str)
			if result != tt.expected {
				t.Errorf("Matches(%q, %q) = %v, want %v", tt.pattern, tt.str, result, tt.expected)
			}
		})
	}
}

// TestMatchWildcardMappingResult calls MatchWildcard with exact and overlapping
// prefix entries. It must choose the longest matching source, retain unmatched
// names, and return the expected match flag for each supplied map.
func TestMatchWildcardMappingResult(t *testing.T) {
	tests := []struct {
		name           string            // mapping subtest display name
		mapping        map[string]string // configured source target pairs
		requestedModel string            // input model for mapping
		expected       string            // expected selected model name
		matched        bool              // expected mapping match flag
	}{
		// This exact key wins over the shorter matching prefix in this map.
		{
			name: "exact match takes precedence",
			mapping: map[string]string{
				"claude-sonnet-4-5": "claude-sonnet-4-5-exact",
				"claude-*":          "claude-default",
			},
			requestedModel: "claude-sonnet-4-5",
			expected:       "claude-sonnet-4-5-exact",
			matched:        true,
		},

		// The longest matching wildcard wins.
		{
			name: "longer wildcard takes precedence",
			mapping: map[string]string{
				"claude-*":         "claude-default",
				"claude-sonnet-*":  "claude-sonnet-default",
				"claude-sonnet-4*": "claude-sonnet-4-series",
			},
			requestedModel: "claude-sonnet-4-5",
			expected:       "claude-sonnet-4-series",
			matched:        true,
		},

		// A single wildcard.
		{
			name: "single wildcard",
			mapping: map[string]string{
				"claude-*": "claude-mapped",
			},
			requestedModel: "claude-opus-4-5",
			expected:       "claude-mapped",
			matched:        true,
		},

		// An unmatched model retains its name.
		{
			name: "no match returns original",
			mapping: map[string]string{
				"claude-*": "claude-mapped",
			},
			requestedModel: "gemini-3-flash",
			expected:       "gemini-3-flash",
			matched:        false,
		},

		// An empty map leaves the model unchanged.
		{
			name:           "empty mapping returns original",
			mapping:        map[string]string{},
			requestedModel: "claude-sonnet-4-5",
			expected:       "claude-sonnet-4-5",
			matched:        false,
		},

		// Gemini model mappings.
		{
			name: "gemini wildcard mapping",
			mapping: map[string]string{
				"gemini-3*":   "gemini-3-pro-high",
				"gemini-2.5*": "gemini-2.5-flash",
			},
			requestedModel: "gemini-3-flash-preview",
			expected:       "gemini-3-pro-high",
			matched:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, matched := MatchWildcard(tt.mapping, tt.requestedModel)
			if result != tt.expected || matched != tt.matched {
				t.Errorf("MatchWildcard(%v, %q) = (%q, %v), want (%q, %v)", tt.mapping, tt.requestedModel, result, matched, tt.expected, tt.matched)
			}
		})
	}
}
