package apikey

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

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

func TestCheckIPRestrictionWithCompiledRules_InvalidWhitelistStillDenies(t *testing.T) {
	// A configured allowlist with no valid patterns must deny access.
	invalidWhitelist := ipmatch.CompileIPRules([]string{"not-a-valid-pattern"})
	allowed, reason := CheckIPRestrictionWithCompiledRules("8.8.8.8", invalidWhitelist, nil)
	require.False(t, allowed)
	require.Equal(t, "access denied", reason)
}
