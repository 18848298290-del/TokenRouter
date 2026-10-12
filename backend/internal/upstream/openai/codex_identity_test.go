package openai

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureCodexIdentityHeaders(t *testing.T) {
	t.Run("补齐缺失身份头", func(t *testing.T) {
		h := make(http.Header)

		EnsureCodexIdentityHeaders(h)
		EnforceCodexIdentityHeaders(h)

		require.Equal(t, CodexDefaultOriginator, h.Get("originator"))
		require.Equal(t, CodexCLIUserAgent, h.Get("user-agent"))
		require.Equal(t, CodexCLIVersion, h.Get("version"))
		require.Equal(t, "responses=experimental", h.Get("OpenAI-Beta"))
	})

	t.Run("保留已有官方UA和合法version并重新配对", func(t *testing.T) {
		const tuiUA = "codex-tui/9.9.9 (Mac OS X 14.0; arm64) iTerm (codex-tui; 9.9.9)"
		h := make(http.Header)
		h.Set("user-agent", tuiUA)
		h.Set("version", "9.9.9")
		h.Set("OpenAI-Beta", "assistants=v2")

		EnsureCodexIdentityHeaders(h)
		EnforceCodexIdentityHeaders(h)

		require.Equal(t, "codex-tui", h.Get("originator"))
		require.Equal(t, tuiUA, h.Get("user-agent"))
		require.Equal(t, "9.9.9", h.Get("version"))
		require.Equal(t, "responses=experimental", h.Get("OpenAI-Beta"))
	})
}

func TestEnforceCodexIdentityHeaders(t *testing.T) {
	const tuiUA = "codex-tui/0.140.2 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.140.2)"

	tests := []struct {
		name           string
		originator     string
		userAgent      string
		version        string
		wantOriginator string
		wantUA         string
		wantVersion    string
	}{
		{
			name:           "错配 originator 按最终 UA 重配",
			originator:     "codex_cli_rs",
			userAgent:      tuiUA,
			wantOriginator: "codex-tui",
			wantUA:         tuiUA,
		},
		{
			name:           "官方配套身份原样保留",
			originator:     "codex-tui",
			userAgent:      tuiUA,
			wantOriginator: "codex-tui",
			wantUA:         tuiUA,
		},
		{
			name:           "第三方 UA 整体回退默认身份",
			originator:     "opencode",
			userAgent:      "luna/1.0.0",
			wantOriginator: CodexDefaultOriginator,
			wantUA:         CodexCLIUserAgent,
		},
		{
			name:           "UA 缺失回退默认身份",
			originator:     "codex_vscode",
			wantOriginator: CodexDefaultOriginator,
			wantUA:         CodexCLIUserAgent,
		},
		{
			name:           "originator override UA 首段被尾部真实身份重写",
			originator:     "cccc",
			userAgent:      "cccc/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)",
			wantOriginator: "codex-tui",
			wantUA:         "codex-tui/0.142.0 (Ubuntu 22.4.0; x86_64) screen (codex-tui; 0.142.0)",
		},
		{
			name:           "低于门槛的 version 提升为内置版本",
			originator:     "codex_cli_rs",
			userAgent:      "codex_cli_rs/0.125.0",
			version:        "0.125.0",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/0.125.0",
			wantVersion:    CodexCLIVersion,
		},
		{
			name:           "达标 version 原样保留",
			originator:     "codex_cli_rs",
			userAgent:      "codex_cli_rs/0.145.0",
			version:        "0.145.0",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/0.145.0",
			wantVersion:    "0.145.0",
		},
		{
			name:           "未携带 version 不注入",
			originator:     "codex_cli_rs",
			userAgent:      "codex_cli_rs/0.98.0",
			wantOriginator: "codex_cli_rs",
			wantUA:         "codex_cli_rs/0.98.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			if tt.originator != "" {
				h.Set("originator", tt.originator)
			}
			if tt.userAgent != "" {
				h.Set("user-agent", tt.userAgent)
			}
			if tt.version != "" {
				h.Set("version", tt.version)
			}

			EnforceCodexIdentityHeaders(h)

			require.Equal(t, tt.wantOriginator, h.Get("originator"))
			require.Equal(t, tt.wantUA, h.Get("user-agent"))
			require.Equal(t, tt.wantVersion, h.Get("version"))
		})
	}
}

// TestEnforceCodexIdentityHeaders_NoOriginatorIsNoop verifies that a missing originator leaves headers unchanged.
func TestEnforceCodexIdentityHeaders_NoOriginatorIsNoop(t *testing.T) {
	h := make(http.Header)
	h.Set("user-agent", "third-party-client/1.0.0")

	EnforceCodexIdentityHeaders(h)

	require.Empty(t, h.Get("originator"))
	require.Equal(t, "third-party-client/1.0.0", h.Get("user-agent"))
}

func TestCodexCanonicalAuthIdentityFollowsResolver(t *testing.T) {
	SetCodexCanonicalUserAgentResolver(func() string {
		return "codex_cli_rs/0.200.1 (Linux; x86_64) terminal"
	})
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	userAgent, originator := CodexCanonicalAuthIdentity()
	require.Equal(t, "codex_cli_rs", originator)
	require.Equal(t, "codex_cli_rs/0.200.1 (Linux; x86_64) terminal", userAgent)
	require.Equal(t, "0.200.1", CodexCanonicalClientVersion())

	h := make(http.Header)
	ApplyCodexCanonicalAuthIdentity(h)
	require.Equal(t, userAgent, h.Get("user-agent"))
	require.Equal(t, originator, h.Get("originator"))
	require.Empty(t, h.Get("version"))
}

func TestCodexCanonicalIdentityFallsBackForInvalidResolverValue(t *testing.T) {
	SetCodexCanonicalUserAgentResolver(func() string { return "not-a-codex-client" })
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(nil) })

	require.Equal(t, CodexCLIUserAgent, CodexCanonicalUserAgent())
	require.Equal(t, CodexCLIVersion, CodexCanonicalClientVersion())
}
