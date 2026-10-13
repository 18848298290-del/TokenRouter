package identity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizePasskeyName supplies blank, padded and oversized Unicode names.
// It requires the default and trimmed values, then checks that truncation counts
// complete runes up to the configured credential-name limit.
func TestNormalizePasskeyName(t *testing.T) {
	require.Equal(t, defaultPasskeyName, normalizePasskeyName("   "))
	require.Equal(t, "Laptop", normalizePasskeyName("  Laptop  "))

	longName := strings.Repeat("密", maxPasskeyNameLength+10)
	// Multibyte fixture characters make byte slicing observable; converting the
	// normalized result back to runes must retain the configured number of complete characters.
	require.Len(t, []rune(normalizePasskeyName(longName)), maxPasskeyNameLength)
}
