package rediscache

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRedeemRateLimitKey formats positive, zero, negative and maximum int64
// user IDs. It checks the exact attempt-counter namespace and decimal identity
// for each supplied case.
func TestRedeemRateLimitKey(t *testing.T) {
	tests := []struct {
		name     string // test case display name
		userID   int64  // supplied user key identifier
		expected string // expected complete Redis key
	}{
		{
			name:     "normal_user_id",
			userID:   123,
			expected: "redeem:ratelimit:123",
		},
		{
			name:     "zero_user_id",
			userID:   0,
			expected: "redeem:ratelimit:0",
		},
		{
			name:     "negative_user_id",
			userID:   -1,
			expected: "redeem:ratelimit:-1",
		},
		{
			name:     "max_int64",
			userID:   math.MaxInt64,
			expected: "redeem:ratelimit:9223372036854775807",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redeemRateLimitKey(tc.userID)
			require.Equal(t, tc.expected, got)
		})
	}
}

// TestRedeemLockKey formats ordinary, empty and punctuation-bearing code text.
// It checks the lock prefix is followed by that exact supplied text without
// code normalization.
func TestRedeemLockKey(t *testing.T) {
	tests := []struct {
		name     string // test case display name
		code     string // supplied exact code text
		expected string // expected complete Redis key
	}{
		{
			name:     "normal_code",
			code:     "ABC123",
			expected: "redeem:lock:ABC123",
		},
		{
			name:     "empty_code",
			code:     "",
			expected: "redeem:lock:",
		},
		{
			name:     "code_with_special_chars",
			code:     "CODE-2024:test",
			expected: "redeem:lock:CODE-2024:test",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redeemLockKey(tc.code)
			require.Equal(t, tc.expected, got)
		})
	}
}
