package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// OAuth failure descriptions collapse CR, LF, and other whitespace before
// entering frontend fragments. These examples exercise text joining and a
// whitespace-only value so the result can be read as one line by the callback
// consumer.

// TestSingleLineStripsWhitespace supplies text split by CRLF and a
// whitespace-only value to OAuthSingleLine. It checks one joined line for the
// first case and empty output for the second.
func TestSingleLineStripsWhitespace(t *testing.T) {
	require.Equal(t, "hello world", OAuthSingleLine("hello\r\nworld"))
	require.Equal(t, "", OAuthSingleLine("\n\t\r"))
}
