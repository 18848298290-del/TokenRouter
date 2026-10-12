package rediscache

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Quota storage keys append the literal supplier identifier to a fixed prefix.
// This formatting case protects that namespace independently of Redis command
// execution, so other fixtures can seed and query the same supplier counter.

// TestQuotaRedisKey_Format formats a Brave supplier counter key without a Redis
// client. Its exact string matches the namespace used by live reservation and
// administrator reset operations.
func TestQuotaRedisKey_Format(t *testing.T) {
	key := quotaRedisKey("brave")
	require.Equal(t, "websearch:quota:brave", key)
}
