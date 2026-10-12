package ipmatch

import (
	"net"
	"testing"
)

// TestCompiledRulesPreserveInvalidPatternCount compiles invalid, trimmed IPv4,
// and IPv6 CIDR patterns. Invalid entries must retain their supplied count,
// compiled entries must match, and the single-pattern helper must reject
// whitespace.
func TestCompiledRulesPreserveInvalidPatternCount(t *testing.T) {
	rules := CompileIPRules([]string{"invalid", ""})
	if rules.PatternCount != 2 || MatchesCompiledRules(net.ParseIP("203.0.113.2"), rules) {
		t.Fatal("invalid patterns must remain present without matching an address")
	}
	rules = CompileIPRules([]string{"2001:db8::/32", " 203.0.113.2 "})
	if !MatchesCompiledRules(net.ParseIP("2001:db8::1"), rules) || !MatchesCompiledRules(net.ParseIP("203.0.113.2"), rules) {
		t.Fatal("compiled IPv4/IPv6 rules must match")
	}
	if MatchesPattern("203.0.113.2", " 203.0.113.2 ") {
		t.Fatal("single-pattern helper must retain its existing strict input behavior")
	}
}
