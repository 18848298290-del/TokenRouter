package apikey

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

// TestCheckIPRestrictionWithCompiledRules supplies overlapping allowed ranges
// and a specifically denied address. It checks that an allowed address succeeds
// and a matching denylist returns the fixed denial reason.
func TestCheckIPRestrictionWithCompiledRules(t *testing.T) {
	whitelist := ipmatch.CompileIPRules([]string{"10.0.0.0/8", "192.168.1.2"})
	blacklist := ipmatch.CompileIPRules([]string{"10.1.1.1"})

	allowed, reason := CheckIPRestrictionWithCompiledRules("10.2.3.4", whitelist, blacklist)
	require.True(t, allowed)
	require.Equal(t, "", reason)

	allowed, reason = CheckIPRestrictionWithCompiledRules("10.1.1.1", whitelist, blacklist)
	require.False(t, allowed)
	require.Equal(t, "access denied", reason)
}

// TestCheckIPRestrictionWithCompiledRules_InvalidWhitelistStillDenies compiles
// an allowlist containing one malformed pattern. It checks that the retained
// configured count rejects an unrelated valid client address despite the empty
// compiled entries.
func TestCheckIPRestrictionWithCompiledRules_InvalidWhitelistStillDenies(t *testing.T) {
	invalidWhitelist := ipmatch.CompileIPRules([]string{"not-a-valid-pattern"})
	allowed, reason := CheckIPRestrictionWithCompiledRules("8.8.8.8", invalidWhitelist, nil)
	require.False(t, allowed)
	require.Equal(t, "access denied", reason)
}
