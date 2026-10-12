package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicBetaTokensContains_EmptyInputs(t *testing.T) {
	require.False(t, AnthropicBetaTokensContains("", "context-management-2025-06-27"))
	require.False(t, AnthropicBetaTokensContains("oauth-2025-04-20", ""))
}

func TestAnthropicBetaTokensContains_SingleToken(t *testing.T) {
	require.True(t, AnthropicBetaTokensContains("context-management-2025-06-27", "context-management-2025-06-27"))
}

func TestAnthropicBetaTokensContains_MultiTokenComma(t *testing.T) {
	header := "oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14"
	require.True(t, AnthropicBetaTokensContains(header, "context-management-2025-06-27"))
	require.True(t, AnthropicBetaTokensContains(header, "oauth-2025-04-20"))
	require.False(t, AnthropicBetaTokensContains(header, "fast-mode-2026-02-01"))
}

func TestAnthropicBetaTokensContains_ToleratesWhitespace(t *testing.T) {
	header := "oauth-2025-04-20 , context-management-2025-06-27 ,  interleaved-thinking-2025-05-14"
	require.True(t, AnthropicBetaTokensContains(header, "context-management-2025-06-27"))
}

func TestAnthropicBetaTokensContains_SubstringNotMatched(t *testing.T) {
	// Match complete tokens, including their version suffix.
	require.False(t, AnthropicBetaTokensContains("context-management-2025-06-27-rev2", "context-management-2025-06-27"),
		"必须按 token 边界匹配，不允许 prefix 子串误命中")
}

func TestSanitizeAnthropicBodyForBetaTokens_NoFieldNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body,
		"oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	require.False(t, changed)
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists(),
		"header 不含 context-management beta 时必须 strip 同名字段")
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaEmpty(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[]},"messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_EmptyBody(t *testing.T) {
	out, changed := SanitizeAnthropicBodyForBetaTokens([]byte{}, "")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = SanitizeAnthropicBodyForBetaTokens(nil, "")
	require.False(t, changed)
	require.Empty(t, out)
}

// TestSanitizeAnthropicBodyForBetaTokens_HaikuRealCCClientPreservesField covers client-enabled context management.

func TestSanitizeAnthropicBodyForBetaTokens_HaikuRealCCClientPreservesField(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	// A Claude Code client supplies the context-management beta.
	clientBeta := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, clientBeta)
	require.False(t, changed,
		"真 CC 客户端 header 含 context-management beta 时，haiku body 字段必须保留（功能不丢）")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_NoFallbackFieldsNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","fallbacks":"default","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body,
		"claude-code-20250219,oauth-2025-04-20,server-side-fallback-2026-07-01")
	require.False(t, changed, "客户端 header 已带 server-side-fallback beta → 字段保留（不过度删除）")
	require.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	require.Equal(t, "default", gjson.GetBytes(out, "fallbacks").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenBetaMissing(t *testing.T) {
	// Clients can supply either "default" or a model list.
	for name, fallbacks := range map[string]string{
		"string_default": `"fallbacks":"default"`,
		"model_array":    `"fallbacks":["claude-opus-4-6","claude-sonnet-4-6"]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"claude-haiku-4-5",` + fallbacks + `,"messages":[]}`)
			// Neither of these headers enables server-side fallback.
			out, changed := SanitizeAnthropicBodyForBetaTokens(body,
				"oauth-2025-04-20,interleaved-thinking-2025-05-14")
			require.True(t, changed)
			require.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
				"header 不含 server-side-fallback beta 时必须 strip fallbacks，否则上游 400")
			require.True(t, gjson.GetBytes(out, "messages").Exists(), "strip 不得误伤其他字段")
		})
	}
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenHeaderEmpty(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallbacks":"default","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallbacks").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenStrippedWhenCreditBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallback_credit_token").Exists(),
		"缺 credit/fallback beta 时必须 strip fallback_credit_token")
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenKeptWithAnyAcceptedBeta(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallback_credit_token":"tok_123","messages":[]}`)
	// Any one of the three accepted betas keeps the credit field.
	for _, beta := range []string{
		BetaServerSideFallback,
		BetaFallbackCredit,
		BetaFallbackCreditLegacy,
	} {
		out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,"+beta)
		require.Falsef(t, changed, "header 含 %s 时 fallback_credit_token 必须保留", beta)
		require.Truef(t, gjson.GetBytes(out, "fallback_credit_token").Exists(),
			"header 含 %s 时 fallback_credit_token 必须保留", beta)
	}
}

// TestSanitizeAnthropicBodyForBetaTokens_StripsFallbacksKeepsContextManagement checks independent field gating.

func TestSanitizeAnthropicBodyForBetaTokens_StripsFallbacksKeepsContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "context-management-2025-06-27")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
		"header 只有 context-management beta → fallbacks 必须 strip")
	require.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"context-management beta 在 header 中 → context_management 不得被误删")
}

func TestSanitizeAnthropicBodyForBetaTokens_KeepsBothWhenBothBetasPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body,
		"context-management-2025-06-27,server-side-fallback-2026-07-01")
	require.False(t, changed, "两个 beta 都在 header 中 → 所有字段保留")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	require.True(t, gjson.GetBytes(out, "fallback_credit_token").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_EmptyBodyUnchanged(t *testing.T) {
	out, changed := SanitizeAnthropicBodyForBetaTokens([]byte{}, "server-side-fallback-2026-07-01")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = SanitizeAnthropicBodyForBetaTokens(nil, "server-side-fallback-2026-07-01")
	require.False(t, changed)
	require.Empty(t, out)
}
