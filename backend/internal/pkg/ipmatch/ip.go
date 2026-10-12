package ipmatch

import (
	"net"
	"slices"
	"strings"
)

// An API-key allowlist can be configured yet contain no valid IP patterns.
// Compilation retains the original pattern count while storing only
// successfully parsed addresses and CIDRs, so
// apikey.CheckIPRestrictionWithCompiledRules can require a match whenever the
// configured count is positive. An absent allowlist therefore differs from an
// invalid configured allowlist.
//
// Compiled matching reuses parsed values and compares addresses using net.IP
// equality. Single-pattern matching and validation parse their inputs as
// supplied, while CompileIPRules trims each pattern. NormalizeIP separately
// handles whitespace and a removable host:port suffix; callers choose that
// normalization before checking restrictions.

// CompiledIPRules stores parsed IP and CIDR rules. PatternCount distinguishes
// an empty rule set from one whose input patterns were all invalid.
type CompiledIPRules struct {
	// CIDRs and IPs contain valid parsed rules, while PatternCount records all
	// supplied patterns. API-key allowlist checks require a match when this count
	// is positive, including when both parsed collections are empty.
	CIDRs        []*net.IPNet // parsed network ranges
	IPs          []net.IP     // parsed exact addresses
	PatternCount int          // original supplied pattern count
}

// CompileIPRules builds reusable matching rules while retaining the count of
// every supplied pattern. Empty or invalid entries contribute to PatternCount
// but contribute no match, so API-key allowlist evaluation can distinguish
// absence from invalid configuration.
//
// Each valid address or CIDR is parsed into storage owned by the result.
// Pattern whitespace is trimmed before parsing, and no validation error is
// returned. Callers treat the result as immutable while sharing it with
// restriction checks.
func CompileIPRules(patterns []string) *CompiledIPRules {
	compiled := &CompiledIPRules{
		CIDRs:        make([]*net.IPNet, 0, len(patterns)),
		IPs:          make([]net.IP, 0, len(patterns)),
		PatternCount: len(patterns),
	}
	// Skip unusable matching values while keeping their configured count. An
	// all-invalid allowlist must remain distinguishable from an omitted allowlist.
	for _, pattern := range patterns {
		normalized := strings.TrimSpace(pattern)
		if normalized == "" {
			continue
		}
		if strings.Contains(normalized, "/") {
			_, cidr, err := net.ParseCIDR(normalized)
			if err != nil || cidr == nil {
				continue
			}
			compiled.CIDRs = append(compiled.CIDRs, cidr)
			continue
		}
		parsedIP := net.ParseIP(normalized)
		if parsedIP == nil {
			continue
		}
		compiled.IPs = append(compiled.IPs, parsedIP)
	}
	return compiled
}

// MatchesCompiledRules reports whether parsedIP matches any parsed address or
// CIDR in rules. Nil inputs return false, and callers supply compiled entries
// whose CIDR pointers remain valid.
func MatchesCompiledRules(parsedIP net.IP, rules *CompiledIPRules) bool {
	if parsedIP == nil || rules == nil {
		return false
	}
	for _, cidr := range rules.CIDRs {
		if cidr.Contains(parsedIP) {
			return true
		}
	}
	return slices.ContainsFunc(rules.IPs, parsedIP.Equal)
}

// MatchesPattern reports whether clientIP matches an exact address or CIDR
// pattern. Both inputs are parsed as supplied, so surrounding whitespace or an
// invalid address or pattern produces false.
func MatchesPattern(clientIP, pattern string) bool {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}

	if strings.Contains(pattern, "/") {
		// Invalid CIDR text cannot establish a match. Return false so restriction
		// callers decide admission using their allowlist or denylist policy.
		_, cidr, err := net.ParseCIDR(pattern)
		if err != nil {
			return false
		}
		return cidr.Contains(ip)
	}

	patternIP := net.ParseIP(pattern)
	if patternIP == nil {
		return false
	}
	return ip.Equal(patternIP)
}

// ValidateIPPattern reports whether the supplied text parses as an address or
// CIDR.
func ValidateIPPattern(pattern string) bool {
	if strings.Contains(pattern, "/") {
		_, _, err := net.ParseCIDR(pattern)
		return err == nil
	}
	return net.ParseIP(pattern) != nil
}

// ValidateIPPatterns returns invalid input patterns in their supplied order
// without trimming. It returns nil when every pattern is valid, including when
// the input list is empty.
func ValidateIPPatterns(patterns []string) []string {
	var invalid []string
	for _, p := range patterns {
		if !ValidateIPPattern(p) {
			invalid = append(invalid, p)
		}
	}
	return invalid
}

// NormalizeIP trims whitespace and returns the host when net.SplitHostPort
// accepts the input. Addresses that lack a separable port retain their trimmed
// text, including malformed strings for later validation.
func NormalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	// A separable host:port value comes from transport addressing. Remove its port
	// before address parsing, while retaining other trimmed text for caller validation.
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return host
	}
	return ip
}

// IsNonPublic reports whether ip is loopback, private, link-local, or
// unspecified. Other reserved ranges and nil addresses return false under these
// net.IP predicates, so this is a limited classification.
func IsNonPublic(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
