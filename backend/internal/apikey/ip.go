package apikey

import (
	"net"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ipmatch"
)

// IP access checks reject an empty or unparsable client address before
// examining any configured rules. A matching denylist entry wins even when the
// same address appears in the allowlist, and a configured allowlist requires a
// successful match. Callers pass compiled rules so repeated authentication uses
// parsed addresses and CIDRs.
//
// Compilation can skip invalid patterns while retaining their original count.
// That count distinguishes an absent allowlist from an allowlist whose entries
// all failed parsing: the latter rejects every address. Address normalization
// accepts surrounding whitespace and removes a port when SplitHostPort can
// separate it; the returned denial text stays fixed for HTTP adapters.

// CheckIPRestrictionWithCompiledRules reports whether clientIP passes the
// compiled IP rules, returning fixed denial text on failure. Callers compile
// configured lists before invoking this function; nil or zero-count rule sets
// impose no list restriction.
//
// The address is normalized and parsed first, then denylist matches take
// precedence over allowlist matches. A positive allowlist PatternCount requires
// a match even when compilation produced no valid patterns, so malformed
// configured lists remain restrictive.
func CheckIPRestrictionWithCompiledRules(clientIP string, whitelist, blacklist *ipmatch.CompiledIPRules) (bool, string) {
	// NormalizeIP accepts HTTP client-address text that can include a port;
	// ParseIP then rejects values that still do not identify an IP address.
	clientIP = ipmatch.NormalizeIP(clientIP)
	if clientIP == "" {
		return false, "access denied"
	}
	parsedIP := net.ParseIP(clientIP)
	if parsedIP == nil {
		return false, "access denied"
	}

	// Explicit denials take precedence when the same address also matches an
	// allowlist entry, so adding an allowlist cannot override a configured denial.
	if blacklist != nil && blacklist.PatternCount > 0 && ipmatch.MatchesCompiledRules(parsedIP, blacklist) {
		return false, "access denied"
	}

	// CompileIPRules retains the supplied pattern count after skipping malformed
	// entries. A configured but wholly invalid allowlist therefore rejects access.
	if whitelist != nil && whitelist.PatternCount > 0 && !ipmatch.MatchesCompiledRules(parsedIP, whitelist) {
		return false, "access denied"
	}

	return true, ""
}
