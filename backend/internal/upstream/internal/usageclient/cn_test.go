package usageclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCNParseF64 supplies numeric values, trimmed numeric strings, invalid
// text, and nil. It checks conversion presence and the accepted numeric
// results within a small floating-point tolerance.
func TestCNParseF64(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string  // named numeric input scenario
		raw  any     // supplied numeric representation value
		want float64 // expected converted numeric value
		ok   bool    // expected numeric conversion presence
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
				// Allow rounding differences within a billionth unit.
				require.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

// TestCNMillisToRFC3339 supplies seconds and milliseconds identifying the same
// instant. It checks a common UTC RFC3339 representation and absence for zero
// and negative epochs.
func TestCNMillisToRFC3339(t *testing.T) {
	t.Parallel()
	// 1700000000 seconds equals 1700000000000 milliseconds.
	want := time.UnixMilli(1700000000000).UTC().Format(time.RFC3339)
	require.Equal(t, want, CnMillisToRFC3339(1700000000))    // seconds
	require.Equal(t, want, CnMillisToRFC3339(1700000000000)) // milliseconds
	require.Equal(t, "", CnMillisToRFC3339(0))               // absent timestamp
	require.Equal(t, "", CnMillisToRFC3339(-1))
}

// TestCnnormalizeResetTime supplies an RFC3339 string and a numeric
// millisecond epoch. It checks UTC formatting for those forms and empty
// results for invalid and empty strings.
func TestCnnormalizeResetTime(t *testing.T) {
	t.Parallel()
	// String timestamps use UTC RFC3339.
	require.Equal(t, "2026-08-14T10:00:00Z", CnNormalizeResetTime("2026-08-14T10:00:00Z"))
	// This float64 fixture represents an epoch in milliseconds.
	require.Equal(t,
		time.UnixMilli(1700000000000).UTC().Format(time.RFC3339),
		CnNormalizeResetTime(float64(1700000000000)))
	// Invalid and empty strings carry no reset time.
	require.Equal(t, "", CnNormalizeResetTime("not-a-time"))
	require.Equal(t, "", CnNormalizeResetTime(""))
}
