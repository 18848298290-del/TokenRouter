package anthropic

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// JSON path rewrites can keep the surrounding top-level key order even when one
// metadata value changes. The test helper locates expected literal tokens in
// the resulting text so identity tests can assert that order alongside their
// rewritten metadata checks.

// assertJSONTokenOrder requires each token's first occurrence to follow the
// preceding token in body. Missing or nonincreasing positions fail through
// require, and t.Helper attributes failures to the calling test. Callers choose
// distinct literal tokens; this check does not parse JSON keys.
func assertJSONTokenOrder(t *testing.T, body string, tokens ...string) {
	t.Helper()

	last := -1
	for _, token := range tokens {
		pos := strings.Index(body, token)
		require.NotEqualf(t, -1, pos, "missing token %s in body %s", token, body)
		require.Greaterf(t, pos, last, "token %s should appear after previous tokens in body %s", token, body)
		last = pos
	}
}
