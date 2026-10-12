package rediscache

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVerifyCodeKey covers empty and punctuated mailbox inputs used as challenge
// keys. The returned key retains plus tags, dots, and subdomains after adding the
// verification prefix for each supplied address.
func TestVerifyCodeKey(t *testing.T) {
	tests := []struct {
		name     string // named key construction case
		email    string // supplied mailbox input text
		expected string // expected Redis key text
	}{
		{
			name:     "normal_email",
			email:    "user@example.com",
			expected: "verify_code:user@example.com",
		},
		{
			name:     "empty_email",
			email:    "",
			expected: "verify_code:",
		},
		{
			name:     "email_with_plus",
			email:    "user+tag@example.com",
			expected: "verify_code:user+tag@example.com",
		},
		{
			name:     "email_with_special_chars",
			email:    "user.name+tag@sub.domain.com",
			expected: "verify_code:user.name+tag@sub.domain.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyCodeKey(tc.email)
			require.Equal(t, tc.expected, got)
		})
	}
}
