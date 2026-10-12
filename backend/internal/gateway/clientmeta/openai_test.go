package clientmeta

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPairCodexClientIdentity exercises leading identities, trailer recovery,
// and invalid names. Pairing keeps originator and the rebuilt leading name
// equal, preserves the suffix, canonicalizes exact names, and rejects unsafe or
// unrecognized identities.
func TestPairCodexClientIdentity(t *testing.T) {
	tests := []struct {
		name           string // test scenario label
		ua             string // input User-Agent value
		wantOriginator string // expected outbound originator
		wantUA         string // expected paired User-Agent
		wantOK         bool   // expected pairing success
	}{
		{
			name:           "cli 首段直接配对",
			ua:             "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color",
			wantOK:         true,
		},
		{
			name:           "tui 首段直接配对",
			ua:             "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)",
			wantOK:         true,
		},
		{
			name:           "Codex 家族前缀配对保留原大小写",
			ua:             "Codex Desktop/1.2.3",
			wantOriginator: "Codex Desktop",
			wantUA:         "Codex Desktop/1.2.3",
			wantOK:         true,
		},
		{
			name:           "originator override 用尾部 name 重写首段",
			ua:             "cccc/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)",
			wantOK:         true,
		},
		{
			name:           "override 尾部恢复保留 Codex 家族真实大小写",
			ua:             "cccc/1.2.3 (Ubuntu 22.4.0; x86_64) term (Codex Desktop; 1.2.3)",
			wantOriginator: "Codex Desktop",
			wantUA:         "Codex Desktop/1.2.3 (Ubuntu 22.4.0; x86_64) term (Codex Desktop; 1.2.3)",
			wantOK:         true,
		},
		{name: "含斜杠的尾部 name 拒绝配对（防自不一致身份）", ua: "foo/1.0 (Codex Desktop/2; 1.0)", wantOK: false},
		{
			name:           "精确集合大小写变体归一为规范小写",
			ua:             "CODEX_CLI_RS/1.0.0",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/1.0.0",
			wantOK:         true,
		},
		{
			name:           "首段尾随空格重建为规范 UA",
			ua:             "codex-tui /1.0.0",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/1.0.0",
			wantOK:         true,
		},
		{name: "家族前缀夹带不可打印字节拒绝", ua: "Codex \x01evil/1.0.0", wantOK: false},
		{name: "家族前缀夹带非 ASCII 字节拒绝", ua: "Codex \xc3\xa9vil/1.0.0", wantOK: false},
		{name: "超长首段拒绝", ua: "Codex " + strings.Repeat("a", 80) + "/1.0.0", wantOK: false},
		{name: "第三方 UA 不可配对", ua: "luna/1.0.0", wantOK: false},
		{name: "伪造前缀不可配对", ua: "codex_cli_rs_evil/1.0.0", wantOK: false},
		{name: "浏览器 UA 不可配对", ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36", wantOK: false},
		{name: "无斜杠不可配对", ua: "curl", wantOK: false},
		{name: "空 UA 不可配对", ua: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originator, pairedUA, ok := PairCodexClientIdentity(tt.ua)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantOriginator, originator)
			require.Equal(t, tt.wantUA, pairedUA)
		})
	}
}

// TestIsCodexCLIRequest supplies historical CLI prefixes inside standalone and
// composite headers. Compatibility matching accepts case and whitespace
// variants and rejects empty headers or unrelated client names.
func TestIsCodexCLIRequest(t *testing.T) {
	tests := []struct {
		name string // test scenario label
		ua   string // input User-Agent value
		want bool   // expected recognition result
	}{
		{name: "codex_cli_rs 前缀", ua: "codex_cli_rs/0.1.0", want: true},
		{name: "codex_vscode 前缀", ua: "codex_vscode/1.2.3", want: true},
		{name: "大小写混合", ua: "Codex_CLI_Rs/0.1.0", want: true},
		{name: "复合 UA 包含 codex", ua: "Mozilla/5.0 codex_cli_rs/0.1.0", want: true},
		{name: "空白包裹", ua: "  codex_vscode/1.2.3  ", want: true},
		{name: "非 codex", ua: "curl/8.0.1", want: false},
		{name: "空字符串", ua: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexCLIRequest(tt.ua)
			if got != tt.want {
				t.Fatalf("IsCodexCLIRequest(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

// TestIsBrowserUserAgent compares Mozilla browser headers with CLI and curl
// values. Browser recognition trims whitespace and ignores prefix case while
// unrelated and empty headers remain false.
func TestIsBrowserUserAgent(t *testing.T) {
	tests := []struct {
		name string // test scenario label
		ua   string // input User-Agent value
		want bool   // expected recognition result
	}{
		{name: "Chrome 浏览器", ua: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36", want: true},
		{name: "大小写混合", ua: "mozilla/5.0 Safari/537.36", want: true},
		{name: "空白包裹", ua: "  Mozilla/5.0 Firefox/126.0  ", want: true},
		{name: "Codex CLI", ua: "codex_cli_rs/0.125.0", want: false},
		{name: "curl", ua: "curl/8.0.1", want: false},
		{name: "空字符串", ua: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBrowserUserAgent(tt.ua)
			if got != tt.want {
				t.Fatalf("IsBrowserUserAgent(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

// TestIsCodexOfficialClientRequest supplies known Codex prefixes, family names,
// embedded markers, and trailer identities. Compatibility recognition accepts
// those forms and rejects unrelated headers or unknown trailer names.
func TestIsCodexOfficialClientRequest(t *testing.T) {
	tests := []struct {
		name string // test scenario label
		ua   string // input User-Agent value
		want bool   // expected recognition result
	}{
		{name: "codex_cli_rs 前缀", ua: "codex_cli_rs/0.98.0", want: true},
		{name: "codex-tui 前缀", ua: "codex-tui/0.125.0", want: true},
		{name: "codex_vscode 前缀", ua: "codex_vscode/1.0.0", want: true},
		{name: "codex_vscode_copilot 前缀", ua: "codex_vscode_copilot/1.0.0", want: true},
		{name: "codex_app 前缀", ua: "codex_app/0.1.0", want: true},
		{name: "codex_chatgpt_desktop 前缀", ua: "codex_chatgpt_desktop/1.0.0", want: true},
		{name: "codex_atlas 前缀", ua: "codex_atlas/1.0.0", want: true},
		{name: "codex_exec 前缀", ua: "codex_exec/0.1.0", want: true},
		{name: "codex_sdk_ts 前缀", ua: "codex_sdk_ts/0.1.0", want: true},
		{name: "Codex 桌面 UA", ua: "Codex Desktop/1.2.3", want: true},
		{name: "复合 UA 包含 codex_app", ua: "Mozilla/5.0 codex_app/0.1.0", want: true},
		{name: "尾部兜底命中 codex-tui", ua: "cccc/0.1.0 (Ubuntu 22.04; x86_64) xterm-256color (codex-tui; 0.125.0)", want: true},
		{name: "大小写混合", ua: "Codex_VSCode/1.2.3", want: true},
		{name: "非 codex", ua: "curl/8.0.1", want: false},
		{name: "非官方尾部", ua: "cccc/0.1.0 (Ubuntu 22.04; x86_64) xterm-256color (not_codex; 0.1.0)", want: false},
		{name: "空字符串", ua: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientRequest(tt.ua)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientRequest(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

// TestIsCodexOfficialClientRequestStrict compares leading and trailer
// identities against markers embedded in browser headers. Strict recognition
// accepts the former and rejects embedded markers alone, unknown names, and
// empty input.
func TestIsCodexOfficialClientRequestStrict(t *testing.T) {
	tests := []struct {
		name string // test scenario label
		ua   string // input User-Agent value
		want bool   // expected recognition result
	}{
		{name: "codex_cli_rs 前缀", ua: "codex_cli_rs/0.98.0", want: true},
		{name: "codex-tui 前缀", ua: "codex-tui/0.125.0", want: true},
		{name: "codex_vscode_copilot 前缀", ua: "codex_vscode_copilot/1.0.0", want: true},
		{name: "codex_exec 前缀", ua: "codex_exec/0.1.0", want: true},
		{name: "Codex 桌面 UA", ua: "Codex Desktop/1.2.3", want: true},
		{name: "尾部兜底命中 codex-tui", ua: "cccc/0.1.0 (Ubuntu 22.04; x86_64) xterm-256color (codex-tui; 0.125.0)", want: true},
		{name: "尾部兜底命中 Codex Desktop", ua: "cccc/0.1.0 (Ubuntu 22.04; x86_64) xterm-256color (Codex Desktop; 0.125.0)", want: true},
		{name: "中段伪造 codex_cli_rs", ua: "Mozilla/5.0 codex_cli_rs/0.1.0", want: false},
		{name: "中段伪造 codex_app", ua: "Mozilla/5.0 codex_app/0.1.0", want: false},
		{name: "evil codex 名称", ua: "evil-codex_cli/1.0.0", want: false},
		{name: "非官方尾部", ua: "cccc/0.1.0 (Ubuntu 22.04; x86_64) xterm-256color (not_codex; 0.1.0)", want: false},
		{name: "空字符串", ua: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientRequestStrict(tt.ua)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientRequestStrict(%q) = %v, want %v", tt.ua, got, tt.want)
			}
		})
	}
}

// TestIsCodexOfficialClientOriginator checks exact known identifiers and Codex
// family names. Whitespace is normalized, while names merely containing Codex
// text and unrelated client identifiers fail recognition.
func TestIsCodexOfficialClientOriginator(t *testing.T) {
	tests := []struct {
		name       string // test scenario label
		originator string // input originator value
		want       bool   // expected recognition result
	}{
		{name: "codex_cli_rs", originator: "codex_cli_rs", want: true},
		{name: "codex-tui", originator: "codex-tui", want: true},
		{name: "codex_vscode", originator: "codex_vscode", want: true},
		{name: "codex_vscode_copilot", originator: "codex_vscode_copilot", want: true},
		{name: "codex_app", originator: "codex_app", want: true},
		{name: "codex_chatgpt_desktop", originator: "codex_chatgpt_desktop", want: true},
		{name: "codex_atlas", originator: "codex_atlas", want: true},
		{name: "codex_exec", originator: "codex_exec", want: true},
		{name: "codex_sdk_ts", originator: "codex_sdk_ts", want: true},
		{name: "Codex 前缀", originator: "Codex Desktop", want: true},
		{name: "空白包裹", originator: "  codex_vscode  ", want: true},
		{name: "evil codex originator", originator: "evil-codex_cli", want: false},
		{name: "未知 codex originator", originator: "my_codex_thing", want: false},
		{name: "非 codex", originator: "my_client", want: false},
		{name: "空字符串", originator: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientOriginator(tt.originator)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientOriginator(%q) = %v, want %v", tt.originator, got, tt.want)
			}
		})
	}
}

// TestIsCodexOfficialClientByHeaders varies whether the User-Agent or
// originator supplies a recognized identity. Either header can establish client
// format, while two unmatched values return false.
func TestIsCodexOfficialClientByHeaders(t *testing.T) {
	tests := []struct {
		name       string // test scenario label
		ua         string // input User-Agent value
		originator string // input originator value
		want       bool   // expected recognition result
	}{
		{name: "仅 originator 命中 desktop", originator: "Codex Desktop", want: true},
		{name: "仅 originator 命中 vscode", originator: "codex_vscode", want: true},
		{name: "仅 ua 命中 desktop", ua: "Codex Desktop/1.2.3", want: true},
		{name: "ua 与 originator 都未命中", ua: "curl/8.0.1", originator: "my_client", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsCodexOfficialClientByHeaders(tt.ua, tt.originator)
			if got != tt.want {
				t.Fatalf("IsCodexOfficialClientByHeaders(%q, %q) = %v, want %v", tt.ua, tt.originator, got, tt.want)
			}
		})
	}
}
