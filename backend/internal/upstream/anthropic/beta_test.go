package anthropic

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeAnthropicBeta(t *testing.T) {
	got := MergeAnthropicBeta(
		[]string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"},
		"foo, oauth-2025-04-20,bar, foo",
	)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,foo,bar", got)
}

func TestMergeAnthropicBeta_EmptyIncoming(t *testing.T) {
	got := MergeAnthropicBeta(
		[]string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"},
		"",
	)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14", got)
}

func TestStripBetaTokens(t *testing.T) {
	tests := []struct {
		name   string
		header string
		tokens []string
		want   string
	}{
		{
			name:   "single token in middle",
			header: "oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "single token at start",
			header: "context-1m-2025-08-07,oauth-2025-04-20,interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "single token at end",
			header: "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-1m-2025-08-07",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "token not present",
			header: "oauth-2025-04-20,interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "empty header",
			header: "",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "",
		},
		{
			name:   "with spaces",
			header: "oauth-2025-04-20, context-1m-2025-08-07 , interleaved-thinking-2025-05-14",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "only token",
			header: "context-1m-2025-08-07",
			tokens: []string{"context-1m-2025-08-07"},
			want:   "",
		},
		{
			name:   "nil tokens",
			header: "oauth-2025-04-20,interleaved-thinking-2025-05-14",
			tokens: nil,
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "multiple tokens removed",
			header: "oauth-2025-04-20,context-1m-2025-08-07,interleaved-thinking-2025-05-14,fast-mode-2026-02-01",
			tokens: []string{"context-1m-2025-08-07", "fast-mode-2026-02-01"},
			want:   "oauth-2025-04-20,interleaved-thinking-2025-05-14",
		},
		{
			name:   "DroppedBetas is empty (filtering moved to configurable beta policy)",
			header: "oauth-2025-04-20,context-1m-2025-08-07,fast-mode-2026-02-01,interleaved-thinking-2025-05-14",
			tokens: DroppedBetas,
			want:   "oauth-2025-04-20,context-1m-2025-08-07,fast-mode-2026-02-01,interleaved-thinking-2025-05-14",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripBetaTokens(tt.header, tt.tokens)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMergeAnthropicBetaDropping_Context1M(t *testing.T) {
	required := []string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"}
	incoming := "context-1m-2025-08-07,foo-beta,oauth-2025-04-20"
	drop := map[string]struct{}{"context-1m-2025-08-07": {}}

	got := MergeAnthropicBetaDropping(required, incoming, drop)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,foo-beta", got)
	require.NotContains(t, got, "context-1m-2025-08-07")
}

func TestMergeAnthropicBetaDropping_DroppedBetas(t *testing.T) {
	required := []string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"}
	incoming := "context-1m-2025-08-07,fast-mode-2026-02-01,foo-beta,oauth-2025-04-20"
	// DroppedBetas is empty; beta policy supplies filtering rules.
	drop := DroppedBetaSet()

	got := MergeAnthropicBetaDropping(required, incoming, drop)
	require.Equal(t, "oauth-2025-04-20,interleaved-thinking-2025-05-14,context-1m-2025-08-07,fast-mode-2026-02-01,foo-beta", got)
	require.Contains(t, got, "context-1m-2025-08-07")
	require.Contains(t, got, "fast-mode-2026-02-01")
}

func TestMergeAnthropicBetaDropping_PreservesIncomingRedactThinking(t *testing.T) {
	required := FullClaudeCodeMimicryBetas()
	incoming := BetaRedactThinking

	got := MergeAnthropicBetaDropping(required, incoming, DroppedBetaSet())

	require.Contains(t, got, BetaRedactThinking)
}

func TestDroppedBetaSet(t *testing.T) {
	// The base set reflects DroppedBetas.
	base := DroppedBetaSet()
	require.Len(t, base, len(DroppedBetas))

	// Caller-supplied tokens extend the set.
	extended := DroppedBetaSet(BetaClaudeCode)
	require.Contains(t, extended, BetaClaudeCode)
	require.Len(t, extended, len(DroppedBetas)+1)
}

func TestBuildBetaTokenSet(t *testing.T) {
	got := BuildBetaTokenSet([]string{"foo", "", "bar", "foo"})
	require.Len(t, got, 2)
	require.Contains(t, got, "foo")
	require.Contains(t, got, "bar")
	require.NotContains(t, got, "")

	empty := BuildBetaTokenSet(nil)
	require.Empty(t, empty)
}

func TestContainsBetaToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		token  string
		want   bool
	}{
		{"present in middle", "oauth-2025-04-20,fast-mode-2026-02-01,interleaved-thinking-2025-05-14", "fast-mode-2026-02-01", true},
		{"present at start", "fast-mode-2026-02-01,oauth-2025-04-20", "fast-mode-2026-02-01", true},
		{"present at end", "oauth-2025-04-20,fast-mode-2026-02-01", "fast-mode-2026-02-01", true},
		{"only token", "fast-mode-2026-02-01", "fast-mode-2026-02-01", true},
		{"not present", "oauth-2025-04-20,interleaved-thinking-2025-05-14", "fast-mode-2026-02-01", false},
		{"with spaces", "oauth-2025-04-20, fast-mode-2026-02-01 , interleaved-thinking-2025-05-14", "fast-mode-2026-02-01", true},
		{"empty header", "", "fast-mode-2026-02-01", false},
		{"empty token", "fast-mode-2026-02-01", "", false},
		{"partial match", "fast-mode-2026-02-01-extra", "fast-mode-2026-02-01", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsBetaToken(tt.header, tt.token)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestStripBetaTokensWithSet_EmptyDropSet(t *testing.T) {
	header := "oauth-2025-04-20,interleaved-thinking-2025-05-14"
	got := StripBetaTokensWithSet(header, map[string]struct{}{})
	require.Equal(t, header, got)
}

func TestFilterBetaTokens(t *testing.T) {
	tokens := []string{"interleaved-thinking-2025-05-14", "tool-search-tool-2025-10-19"}
	filterSet := map[string]struct{}{
		"tool-search-tool-2025-10-19": {},
	}

	assert.Equal(t, []string{"interleaved-thinking-2025-05-14"}, FilterBetaTokens(tokens, filterSet))
	assert.Equal(t, tokens, FilterBetaTokens(tokens, nil))
	assert.Nil(t, FilterBetaTokens(nil, filterSet))
}

func TestComputeFinalAnthropicBeta_OAuthMimic_NonHaiku_IncludesContextManagement(t *testing.T) {
	s := false
	final, ok := ComputeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", http.Header{}, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, BetaContextManagement),
		"OAuth mimic non-haiku 必须注入完整 CC mimicry beta，含 context-management-2025-06-27")
	require.True(t, AnthropicBetaTokensContains(final, BetaOAuth))
	require.True(t, AnthropicBetaTokensContains(final, BetaClaudeCode))
}

func TestComputeFinalAnthropicBeta_OAuthMimic_Haiku_IncludesFullClaudeCodeBetas(t *testing.T) {
	s := false
	final, ok := ComputeFinalAnthropicBeta("oauth", true, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.Equal(t, strings.Join(FullClaudeCodeMimicryBetas(), ","), final)
	for _, beta := range FullClaudeCodeMimicryBetas() {
		require.Truef(t, AnthropicBetaTokensContains(final, beta),
			"OAuth mimic Haiku 必须包含完整 Claude Code beta 集合，缺少 %s", beta)
	}
}

func TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBeta(t *testing.T) {
	// Messages mimic ignores client betas.
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta")
	final, ok := ComputeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.False(t, strings.Contains(final, "custom-experimental-beta"),
		"mimic 路径必须忽略客户端 anthropic-beta header")
}

func TestComputeFinalAnthropicBeta_OAuthTransparent_NonHaiku_PreservesClientContextManagement(t *testing.T) {
	// Passthrough retains the client-enabled context-management beta.
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27")
	final, ok := ComputeFinalAnthropicBeta("oauth", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, BetaContextManagement))
}

func TestComputeFinalAnthropicBeta_OAuthTransparent_Haiku_RealCCPreservesContextManagement(t *testing.T) {
	// Haiku retains the beta supplied by the client.
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	final, ok := ComputeFinalAnthropicBeta("oauth", false, "claude-haiku-4-5", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, BetaContextManagement),
		"真 CC + haiku + 客户端带 context-management beta → 透传必须保留")
}

func TestComputeFinalAnthropicBeta_APIKey_PassesClientBetaThroughDropSet(t *testing.T) {
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "oauth-2025-04-20,custom-beta")
	final, ok := ComputeFinalAnthropicBeta("apikey", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, "oauth-2025-04-20"))
	require.True(t, AnthropicBetaTokensContains(final, "custom-beta"))
}

func TestComputeFinalAnthropicBeta_APIKey_NoClientBetaInjectOff_ShouldNotSet(t *testing.T) {
	s := false
	final, ok := ComputeFinalAnthropicBeta("apikey", false, "claude-sonnet-4-6", http.Header{}, []byte(`{}`), nil, s)
	require.False(t, ok, "API-key + 客户端未传 + InjectBetaForAPIKey 关 → 不应主动设置 anthropic-beta")
	require.Equal(t, "", final)
}

func TestComputeFinalAnthropicBeta_APIKeyHaiku_StillUsesAPIKeyBetas(t *testing.T) {
	s := true
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled"},"messages":[]}`)
	final, ok := ComputeFinalAnthropicBeta("apikey", false, "claude-haiku-4-5", http.Header{}, body, nil, s)
	require.True(t, ok)
	require.Equal(t, APIKeyHaikuBetaHeader, final)
	require.False(t, AnthropicBetaTokensContains(final, BetaOAuth))
	require.False(t, AnthropicBetaTokensContains(final, BetaClaudeCode))
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_AlwaysIncludesContextManagement(t *testing.T) {
	// Count-token mimic includes the mimic set and token-counting beta.
	s := false
	final, ok := ComputeFinalCountTokensAnthropicBeta("oauth", true, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, BetaContextManagement),
		"count_tokens + mimic Haiku 必须保留 context-management beta")
	require.True(t, AnthropicBetaTokensContains(final, BetaTokenCounting),
		"count_tokens 路径必须含 token-counting beta")
}

// TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_PreservesClientBeta checks client beta merging.

func TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_PreservesClientBeta(t *testing.T) {
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta,context-1m-2025-08-07")
	final, ok := ComputeFinalCountTokensAnthropicBeta("oauth", true, "claude-haiku-4-5", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, "custom-experimental-beta"),
		"count_tokens mimic 不同于 messages mimic：原代码会保留客户端透传的 beta")
	require.True(t, AnthropicBetaTokensContains(final, "context-1m-2025-08-07"),
		"客户端透传的其他 beta token 同样需要保留")
	require.True(t, AnthropicBetaTokensContains(final, BetaContextManagement),
		"同时 FullClaudeCodeMimicryBetas 不打折扣")
	require.True(t, AnthropicBetaTokensContains(final, BetaTokenCounting),
		"同时补齐 token-counting beta")
}

// TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBetaExplicit checks the built-in Messages mimic set.

func TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBetaExplicit(t *testing.T) {
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta")
	final, ok := ComputeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.False(t, AnthropicBetaTokensContains(final, "custom-experimental-beta"),
		"messages mimic 原代码跳过白名单透传 → 客户端 beta 不进入计算。"+
			"与 count_tokens mimic 是不同的设计，不能合并为同一函数。")
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_NoClientBetaInjectsDefault(t *testing.T) {
	// A missing client header selects CountTokensBetaHeader.
	s := false
	final, ok := ComputeFinalCountTokensAnthropicBeta("oauth", false, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.Equal(t, CountTokensBetaHeader, final)
	// The default count-token header omits context management.
	require.False(t, AnthropicBetaTokensContains(final, BetaContextManagement))
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_AppendsBetaTokenCounting(t *testing.T) {
	s := false
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "oauth-2025-04-20,context-management-2025-06-27")
	final, ok := ComputeFinalCountTokensAnthropicBeta("oauth", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil, s)
	require.True(t, ok)
	require.True(t, AnthropicBetaTokensContains(final, BetaTokenCounting),
		"客户端未带 token-counting beta 时必须补齐")
	require.True(t, AnthropicBetaTokensContains(final, BetaContextManagement),
		"客户端带的 context-management beta 必须保留")
}
