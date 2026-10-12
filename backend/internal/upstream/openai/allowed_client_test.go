package openai

import "testing"

// The plugin derives both headers from clientInfo.name="Claude Code".
const (
	testClaudeCodeOriginator = "Claude Code"
	testClaudeCodeUserAgent  = "Claude Code/0.5.0 (Macos 15.5; arm64) iTerm2.app (Claude Code; 1.0.4)"
)

func TestIsAllowedClientMatch(t *testing.T) {
	entry := AllowedClientEntry{Originator: "Claude Code", UAContains: []string{"Claude Code/"}}

	tests := []struct {
		name       string
		ua         string
		originator string
		want       bool
	}{
		{name: "真实签名命中", ua: testClaudeCodeUserAgent, originator: testClaudeCodeOriginator, want: true},
		{name: "大小写不敏感", ua: "claude code/0.5.0 (macos)", originator: "claude code", want: true},
		{name: "originator 两侧空白被裁剪", ua: testClaudeCodeUserAgent, originator: "  Claude Code  ", want: true},
		{name: "originator 非精确（带后缀）不命中", ua: testClaudeCodeUserAgent, originator: "Claude Code Extra", want: false},
		{name: "originator 为空不命中", ua: testClaudeCodeUserAgent, originator: "", want: false},
		{name: "originator 是官方 codex 不命中", ua: testClaudeCodeUserAgent, originator: "codex_cli_rs", want: false},
		{name: "UA 缺少 Claude Code/ 标记不命中", ua: "curl/8.0", originator: testClaudeCodeOriginator, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAllowedClientMatch(tt.ua, tt.originator, entry); got != tt.want {
				t.Fatalf("IsAllowedClientMatch(%q, %q) = %v, want %v", tt.ua, tt.originator, got, tt.want)
			}
		})
	}
}

func TestIsAllowedClientMatch_EmptyOriginatorEntryNeverMatches(t *testing.T) {
	// An absent originator must not turn the preset into a permissive match.
	entry := AllowedClientEntry{Originator: "", UAContains: []string{"Claude Code/"}}
	if IsAllowedClientMatch(testClaudeCodeUserAgent, "", entry) {
		t.Fatal("空 Originator 的条目不应匹配任何请求")
	}
}

func TestIsAllowedClientMatch_EmptyUAContainsNeverMatches(t *testing.T) {
	// Missing UA requirements must not allow originator-only matching.
	entry := AllowedClientEntry{Originator: "Claude Code", UAContains: nil}
	if IsAllowedClientMatch(testClaudeCodeUserAgent, testClaudeCodeOriginator, entry) {
		t.Fatal("未声明 UA 特征的预设不应匹配，避免退化为单因子 originator 匹配")
	}
}

func TestIsAllowedClientMatch_WhitespaceUAMarkerNeverMatches(t *testing.T) {
	// Whitespace markers are invalid after normalization and must fail closed.
	entry := AllowedClientEntry{Originator: "Claude Code", UAContains: []string{"   "}}
	if IsAllowedClientMatch(testClaudeCodeUserAgent, testClaudeCodeOriginator, entry) {
		t.Fatal("UAContains 含全空白 marker 不应匹配，避免退化为单因子 originator 匹配")
	}
}

func TestIsAllowedClientMatch_MixedEmptyUAMarkerNeverMatches(t *testing.T) {
	// One valid marker does not make a preset with a blank marker valid.
	entry := AllowedClientEntry{Originator: "Claude Code", UAContains: []string{"", "Claude Code/"}}
	if IsAllowedClientMatch(testClaudeCodeUserAgent, testClaudeCodeOriginator, entry) {
		t.Fatal("UAContains 混入空白 marker 不应匹配")
	}
}

func TestMatchAllowedClients(t *testing.T) {
	tests := []struct {
		name       string
		ua         string
		originator string
		clientIDs  []string
		want       bool
	}{
		{name: "claude_code 预设命中真实签名", ua: testClaudeCodeUserAgent, originator: testClaudeCodeOriginator, clientIDs: []string{AllowedClientClaudeCode}, want: true},
		{name: "claude_code 预设 + 伪造 originator 不命中", ua: testClaudeCodeUserAgent, originator: "my_client", clientIDs: []string{AllowedClientClaudeCode}, want: false},
		{name: "空列表不放行", ua: testClaudeCodeUserAgent, originator: testClaudeCodeOriginator, clientIDs: nil, want: false},
		{name: "未知预设 ID 不放行", ua: testClaudeCodeUserAgent, originator: testClaudeCodeOriginator, clientIDs: []string{"unknown_client"}, want: false},
		{name: "ID 大小写/空白容错", ua: testClaudeCodeUserAgent, originator: testClaudeCodeOriginator, clientIDs: []string{"  Claude_Code "}, want: true},
		{name: "多预设任一命中即放行", ua: testClaudeCodeUserAgent, originator: testClaudeCodeOriginator, clientIDs: []string{"unknown_client", AllowedClientClaudeCode}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchAllowedClients(tt.ua, tt.originator, tt.clientIDs); got != tt.want {
				t.Fatalf("MatchAllowedClients(%q, %q, %v) = %v, want %v", tt.ua, tt.originator, tt.clientIDs, got, tt.want)
			}
		})
	}
}
