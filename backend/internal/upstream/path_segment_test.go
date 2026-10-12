package upstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSanitizedUpstreamPathSuffixRejectsNonConformingSegments supplies
// navigation, separator, punctuation, and Unicode suffixes alongside valid
// identifiers. It checks rejection returns empty and accepted paths retain
// their supplied spelling.
func TestSanitizedUpstreamPathSuffixRejectsNonConformingSegments(t *testing.T) {
	// The validator receives URL.Path after URL parsing. Include decoded
	// structural characters and literal percent text so neither form can alter
	// the constructed upstream path.
	rejected := []string{
		"/..",
		"/../..",
		"/../../x/y",
		"/./compact",
		"/compact/..",
		`/..\..\x`,
		`/compact\..`,
		"/?a=b",
		"/compact?a=b",
		"/compact#frag",
		"/compact%2f..",
		"/100%",
		"//double",
		"/compact//detail",
		"/compact/",
		"/ compact",
		"/compact\x00",
		"/compact\nX-Injected: 1",
		"/模型",
		"compact",
		"/a:b",
		"/a;b",
		"/a,b",
		"/a=b",
		"/a&b",
		// Only ASCII letters, digits, underscore, hyphen, and dot are accepted.
		"/a~b",
		"/a@b",
		"/a+b",
		"/a|b",
		"/a*b",
		"/a$b",
		"/a(b)",
		"/a'b",
		"/a\"b",
		"/a<b",
		"/a\tb",
		"/a b",
		"/a∕b", // DIVISION SLASH
		"/a／b", // FULLWIDTH SOLIDUS
		// These dot-only spellings cover normalization differences between URL and
		// proxy handling; accepting any of them would permit an ambiguous request path.
		"/...",
		"/....",
		"/compact/...",
	}
	for _, suffix := range rejected {
		t.Run("reject_"+suffix, func(t *testing.T) {
			got, ok := SanitizedUpstreamPathSuffix(suffix)
			require.False(t, ok, "suffix %q must be rejected", suffix)
			require.Empty(t, got)
		})
	}

	accepted := map[string]string{
		"":                          "",
		"/compact":                  "/compact",
		"/compact/detail":           "/compact/detail",
		"/resp_68f0a1b2c3d4/cancel": "/resp_68f0a1b2c3d4/cancel",
		"/gemini-2.5-pro_v1.2":      "/gemini-2.5-pro_v1.2",
		"/a.b.c":                    "/a.b.c",
	}
	for suffix, want := range accepted {
		t.Run("accept_"+suffix, func(t *testing.T) {
			got, ok := SanitizedUpstreamPathSuffix(suffix)
			require.True(t, ok, "suffix %q must be accepted", suffix)
			require.Equal(t, want, got)
		})
	}
}

// TestSanitizedUpstreamPathSuffixEnforcesBounds builds one segment and one
// suffix deeper than their respective limits. It checks that both excessive
// inputs are rejected before forwarding.
func TestSanitizedUpstreamPathSuffixEnforcesBounds(t *testing.T) {
	longSegment := "/"
	for range MaxUpstreamPathSegmentLen + 1 {
		longSegment += "a"
	}
	_, ok := SanitizedUpstreamPathSuffix(longSegment)
	require.False(t, ok, "over-long segment must be rejected")

	deep := ""
	for i := 0; i <= MaxUpstreamPathSegments; i++ {
		deep += "/a"
	}
	_, ok = SanitizedUpstreamPathSuffix(deep)
	require.False(t, ok, "over-deep suffix must be rejected")
}
