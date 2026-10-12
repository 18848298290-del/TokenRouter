package usageclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Accept JSON numbers and numeric strings because cc-switch and upstream field types differ.
func TestCNParseF64(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  any
		want float64
		ok   bool
	}{
		{"float64", float64(12.5), 12.5, true},
		{"int", 100, 100, true},
		{"numeric string", "33.3", 33.3, true},
		{"trim string", "  7  ", 7, true},
		{"non-numeric string", "abc", 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := CnParseF64(tc.raw)
			require.Equal(t, tc.ok, ok)
			if ok {
				require.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

// Seconds and milliseconds identify the same instant; nonpositive timestamps are absent.
func TestCNMillisToRFC3339(t *testing.T) {
	t.Parallel()
	// 1700000000 seconds equals 1700000000000 milliseconds.
	want := time.UnixMilli(1700000000000).UTC().Format(time.RFC3339)
	require.Equal(t, want, CnMillisToRFC3339(1700000000))    // seconds
	require.Equal(t, want, CnMillisToRFC3339(1700000000000)) // milliseconds
	require.Equal(t, "", CnMillisToRFC3339(0))               // absent timestamp
	require.Equal(t, "", CnMillisToRFC3339(-1))
}

// Normalize timestamp strings and numeric units; reject invalid input.
func TestCnnormalizeResetTime(t *testing.T) {
	t.Parallel()
	// String timestamps use UTC RFC3339.
	require.Equal(t, "2026-08-14T10:00:00Z", CnNormalizeResetTime("2026-08-14T10:00:00Z"))
	// JSON numbers arrive as float64 milliseconds.
	require.Equal(t,
		time.UnixMilli(1700000000000).UTC().Format(time.RFC3339),
		CnNormalizeResetTime(float64(1700000000000)))
	// Invalid and empty strings carry no reset time.
	require.Equal(t, "", CnNormalizeResetTime("not-a-time"))
	require.Equal(t, "", CnNormalizeResetTime(""))
}
