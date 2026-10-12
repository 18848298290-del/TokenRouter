package provider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Supplier error excerpts use byte counts with an optional truncation marker.
// ASCII fixtures below, at, and beyond the limit check the exact short-string
// result and the long result's length and suffix, while multibyte validity
// remains outside these assertions.

// TestTruncateBody_Short passes a short ASCII diagnostic body to excerpt
// formatting. The returned string equals the full input, protecting content
// retention when the body stays under the cap.
func TestTruncateBody_Short(t *testing.T) {
	body := []byte("short body")
	require.Equal(t, "short body", truncateBody(body))
}

// TestTruncateBody_Long passes five hundred ASCII bytes beyond the excerpt cap.
// Output length includes the capped prefix and marker, and the returned string
// has the asserted truncation suffix.
func TestTruncateBody_Long(t *testing.T) {
	body := []byte(strings.Repeat("x", 500))
	result := truncateBody(body)
	require.Len(t, result, errorBodyTruncLen+len("...(truncated)"))
	require.True(t, strings.HasSuffix(result, "...(truncated)"))
}

// TestTruncateBody_ExactBoundary passes an ASCII body whose byte count equals
// the excerpt cap. The returned string matches its entire input and therefore
// gains no truncation marker.
func TestTruncateBody_ExactBoundary(t *testing.T) {
	body := []byte(strings.Repeat("x", errorBodyTruncLen))
	require.Equal(t, string(body), truncateBody(body))
}
