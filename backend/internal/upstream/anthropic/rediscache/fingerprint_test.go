package rediscache

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFingerprintKey formats normal, zero, negative, and maximal provider
// identifiers into Redis keys. Each value retains its signed decimal text after
// the fingerprint namespace prefix.
func TestFingerprintKey(t *testing.T) {
	tests := []struct {
		name       string // case display name
		providerID int64  // input provider identifier
		expected   string // expected comparison result
	}{
		{
			name:       "normal_account_id",
			providerID: 123,
			expected:   "fingerprint:123",
		},
		{
			name:       "zero_account_id",
			providerID: 0,
			expected:   "fingerprint:0",
		},
		{
			name:       "negative_account_id",
			providerID: -1,
			expected:   "fingerprint:-1",
		},
		{
			name:       "max_int64",
			providerID: math.MaxInt64,
			expected:   "fingerprint:9223372036854775807",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fingerprintKey(tc.providerID)
			require.Equal(t, tc.expected, got)
		})
	}
}
