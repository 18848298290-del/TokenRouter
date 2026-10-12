package ipmatch

import (
	"net"
	"slices"
	"strings"
)

// CompiledIPRules stores parsed IP and CIDR rules. PatternCount distinguishes
// an empty rule set from one whose input patterns were all invalid.
type CompiledIPRules struct {
	CIDRs        []*net.IPNet
	IPs          []net.IP
	PatternCount int
}

// CompileIPRules trims and parses patterns, skipping invalid entries
// while retaining the original input count.
func CompileIPRules(patterns []string) *CompiledIPRules {
	compiled := &CompiledIPRules{
		CIDRs:        make([]*net.IPNet, 0, len(patterns)),
		IPs:          make([]net.IP, 0, len(patterns)),
		PatternCount: len(patterns),
	}
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

// MatchesPattern matches clientIP against an exact IP or CIDR pattern.
// Inputs are parsed as supplied; surrounding whitespace is not trimmed.
func MatchesPattern(clientIP, pattern string) bool {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}

	if strings.Contains(pattern, "/") {
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

// ValidateIPPattern reports whether pattern is an IP address or CIDR.
func ValidateIPPattern(pattern string) bool {
	if strings.Contains(pattern, "/") {
		_, _, err := net.ParseCIDR(pattern)
		return err == nil
	}
	return net.ParseIP(pattern) != nil
}

// ValidateIPPatterns returns invalid patterns in input order.
func ValidateIPPatterns(patterns []string) []string {
	var invalid []string
	for _, p := range patterns {
		if !ValidateIPPattern(p) {
			invalid = append(invalid, p)
		}
	}
	return invalid
}

// NormalizeIP trims whitespace and removes a port when net.SplitHostPort succeeds.
func NormalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return host
	}
	return ip
}

// IsNonPublic reports loopback, private, link-local, and unspecified addresses.
// It does not classify every reserved or non-routable address range.
func IsNonPublic(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
