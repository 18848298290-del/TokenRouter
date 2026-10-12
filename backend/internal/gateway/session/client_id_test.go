package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSanitizeSessionID exercises correlation IDs at the Unicode column limit and
// with malformed bytes or control characters. Accepted IDs retain their trimmed
// spelling, while invalid or oversized IDs return empty.
func TestSanitizeSessionID(t *testing.T) {
	longRunes := strings.Repeat("a", MaxPersistedSessionIDLength+50)
	multibyte := strings.Repeat("好", MaxPersistedSessionIDLength+10)

	tests := []struct {
		name string // named validation fixture
		in   string // supplied client session bytes
		want string // expected accepted session value
	}{
		{"empty", "", ""},
		{"whitespace only", "   \t  ", ""},
		{"trims surrounding whitespace", "  sess-123  ", "sess-123"},
		{"plain value", "conv_abc-123.XYZ", "conv_abc-123.XYZ"},
		{"uuid", "550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"reject CR", "sess\r123", ""},
		{"reject LF", "sess\n123", ""},
		{"reject CRLF injection", "sess-1\r\nSet-Cookie: x=y", ""},
		{"reject tab inside", "sess\t123", ""},
		{"reject NUL", "sess\x00123", ""},
		{"reject DEL", "sess\x7f123", ""},
		{"reject invalid UTF-8", string([]byte{'s', 'e', 's', 's', '-', 0xff}), ""},
		{"accepts value at column bound", strings.Repeat("b", MaxPersistedSessionIDLength), strings.Repeat("b", MaxPersistedSessionIDLength)},
		{"rejects overlong value", longRunes, ""},
		{"rejects overlong multibyte value", multibyte, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeClientSessionID(tc.in)
			require.Equal(t, tc.want, got, "SanitizeClientSessionID(%q)", tc.in)
			// Count runes because PostgreSQL limits this correlation column by
			// characters, including accepted multibyte IDs rather than UTF-8 bytes.
			require.LessOrEqual(t, len([]rune(got)), MaxPersistedSessionIDLength)
		})
	}
}
