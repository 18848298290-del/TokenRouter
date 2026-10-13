package provider

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TestDuplicateProviderNamePreservesSuffixWithinSchemaLimit copies a label
// containing one hundred multibyte characters. The helper's result contains one
// hundred runes and retains the complete copy suffix without splitting source
// characters.
func TestDuplicateProviderNamePreservesSuffixWithinSchemaLimit(t *testing.T) {
	name := duplicateProviderName(strings.Repeat("界", 100))

	require.Equal(t, 100, utf8.RuneCountInString(name))
	require.True(t, strings.HasSuffix(name, " (Copy)"))
}
