package moderation

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContentModerationKeywordMatcherMatchesLegacyBehavior compares indexed
// matching with the sequential substring helper across ordered, overlapping,
// Unicode, duplicate, and empty-entry cases. It checks both the returned
// spelling and hit flag.
func TestContentModerationKeywordMatcherMatchesLegacyBehavior(t *testing.T) {
	tests := []struct {
		name     string   // selected parity scenario name
		text     string   // text supplied to scanners
		keywords []string // ordered configured keyword spellings
	}{
		{name: "miss", text: "clean prompt", keywords: []string{"blocked", "secret"}},
		{name: "case insensitive", text: "contains SECRET value", keywords: []string{"secret"}},
		{name: "configured order wins", text: "early appears before later", keywords: []string{"later", "early"}},
		{name: "overlap uses configured order", text: "abc", keywords: []string{"bc", "abc"}},
		{name: "unicode", text: "这里包含敏感词和世界", keywords: []string{"世界", "敏感词"}},
		{name: "duplicates", text: "duplicate", keywords: []string{"duplicate", "DUPLICATE"}},
		{name: "empty entries", text: "blocked", keywords: []string{"", "blocked"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantKeyword, wantHit := matchBlockedKeyword(tt.text, tt.keywords)
			gotKeyword, gotHit := newContentModerationKeywordMatcher(tt.keywords).Match(tt.text)
			require.Equal(t, wantHit, gotHit)
			require.Equal(t, wantKeyword, gotKeyword)
		})
	}
}

// TestContentModerationKeywordMatcherRandomizedParity generates reproducible
// mixed-case ASCII inputs and keyword lists from a fixed seed. It checks that
// one thousand indexed scans return the same hit flag and configured spelling
// as sequential scans.
func TestContentModerationKeywordMatcherRandomizedParity(t *testing.T) {
	rng := rand.New(rand.NewSource(20260714))
	// Mixed-case ASCII generates case-normalization parity inputs reproducibly.
	const alphabet = "abcXYZ"
	// Use a bounded generated sample alongside the explicit overlap and Unicode cases.
	for iteration := range 1000 {
		keywords := make([]string, 1+rng.Intn(30))
		for index := range keywords {
			length := 1 + rng.Intn(8)
			var value strings.Builder
			for range length {
				// Builder.WriteByte returns a nil error after appending this byte; discard
				// that result while constructing the seeded keyword input used by both scanners.
				_ = value.WriteByte(alphabet[rng.Intn(len(alphabet))])
			}
			keywords[index] = value.String()
		}
		var text strings.Builder
		for range 20 + rng.Intn(100) {
			// Discard WriteByte's nil error while constructing the generated scan text;
			// strings.Builder appends every supplied byte before returning to this loop.
			_ = text.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}

		wantKeyword, wantHit := matchBlockedKeyword(text.String(), keywords)
		gotKeyword, gotHit := newContentModerationKeywordMatcher(keywords).Match(text.String())
		require.Equal(t, wantHit, gotHit, "iteration %d", iteration)
		require.Equal(t, wantKeyword, gotKeyword, "iteration %d", iteration)
	}
}
