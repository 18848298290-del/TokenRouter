package apikey

import (
	"net"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

// CheckIPRestrictionWithCompiledRules checks the denylist before requiring a match in a configured allowlist.
func CheckIPRestrictionWithCompiledRules(clientIP string, whitelist, blacklist *ipmatch.CompiledIPRules) (bool, string) {
	// Trim whitespace and remove a port before parsing the address.
	clientIP = ipmatch.NormalizeIP(clientIP)
	if clientIP == "" {
		return false, "access denied"
	}
	parsedIP := net.ParseIP(clientIP)
	if parsedIP == nil {
		return false, "access denied"
	}

	// A denylist match takes precedence over the allowlist.
	if blacklist != nil && blacklist.PatternCount > 0 && ipmatch.MatchesCompiledRules(parsedIP, blacklist) {
		return false, "access denied"
	}

	// A configured allowlist must match, even if all its patterns are invalid.
	if whitelist != nil && whitelist.PatternCount > 0 && !ipmatch.MatchesCompiledRules(parsedIP, whitelist) {
		return false, "access denied"
	}

	return true, ""
}
