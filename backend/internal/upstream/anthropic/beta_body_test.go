package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestAnthropicBetaTokensContains_EmptyInputs supplies an empty Header and an
// empty sought token in separate checks. Each input reports false so missing
// data cannot enable a gated request field.
func TestAnthropicBetaTokensContains_EmptyInputs(t *testing.T) {
	require.False(t, AnthropicBetaTokensContains("", "context-management-2025-06-27"))
	require.False(t, AnthropicBetaTokensContains("oauth-2025-04-20", ""))
}

// TestAnthropicBetaTokensContains_SingleToken uses one complete
// context-management token as the whole Header. Exact equality reports true so
// a lone accepted token can retain its corresponding field.
func TestAnthropicBetaTokensContains_SingleToken(t *testing.T) {
	require.True(t, AnthropicBetaTokensContains("context-management-2025-06-27", "context-management-2025-06-27"))
}

// TestAnthropicBetaTokensContains_MultiTokenComma looks up present and absent
// tokens in a comma-separated Header. Membership succeeds for complete entries
// and fails for a different capability that the list lacks.
func TestAnthropicBetaTokensContains_MultiTokenComma(t *testing.T) {
	header := "oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14"
	require.True(t, AnthropicBetaTokensContains(header, "context-management-2025-06-27"))
	require.True(t, AnthropicBetaTokensContains(header, "oauth-2025-04-20"))
	require.False(t, AnthropicBetaTokensContains(header, "fast-mode-2026-02-01"))
}

// TestAnthropicBetaTokensContains_ToleratesWhitespace places spaces around
// comma-separated beta entries in a client Header. The complete
// context-management token still matches after entry trimming so whitespace
// does not disable its gate.
func TestAnthropicBetaTokensContains_ToleratesWhitespace(t *testing.T) {
	header := "oauth-2025-04-20 , context-management-2025-06-27 ,  interleaved-thinking-2025-05-14"
	require.True(t, AnthropicBetaTokensContains(header, "context-management-2025-06-27"))
}

// TestAnthropicBetaTokensContains_SubstringNotMatched looks for a base token
// inside a longer revision token. Membership remains false so sharing a prefix
// does not grant the capability represented by the shorter token.
func TestAnthropicBetaTokensContains_SubstringNotMatched(t *testing.T) {
	// Match complete tokens, including their version suffix.
	require.False(t, AnthropicBetaTokensContains("context-management-2025-06-27-rev2", "context-management-2025-06-27"),
		"必须按 token 边界匹配，不允许 prefix 子串误命中")
}

// TestSanitizeAnthropicBodyForBetaTokens_NoFieldNoChange provides a body
// containing no beta-gated extension fields and an OAuth Header. It reports no
// change and retains the exact request bytes.
func TestSanitizeAnthropicBodyForBetaTokens_NoFieldNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

// TestSanitizeAnthropicBodyForBetaTokens_FieldKeptWhenBetaPresent supplies
// context management with its complete token among other client betas. The
// field and edit type remain present, and the changed flag stays false.
func TestSanitizeAnthropicBodyForBetaTokens_FieldKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body,
		"oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	require.False(t, changed)
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

// TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaMissing supplies
// context management with OAuth and thinking tokens but no context token.
// Successful sanitation reports a change and removes the gated field from the
// body.
func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists(),
		"header 不含 context-management beta 时必须 strip 同名字段")
}

// TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaEmpty supplies
// context management with an empty final beta Header. Sanitation reports a
// change and deletes the field because no accepted context-management token
// remains.
func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaEmpty(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[]},"messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

// TestSanitizeAnthropicBodyForBetaTokens_EmptyBody passes both an empty slice
// and nil with an empty Header. Each result stays empty and reports false so
// absent request bytes do not claim successful deletion.
func TestSanitizeAnthropicBodyForBetaTokens_EmptyBody(t *testing.T) {
	out, changed := SanitizeAnthropicBodyForBetaTokens([]byte{}, "")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = SanitizeAnthropicBodyForBetaTokens(nil, "")
	require.False(t, changed)
	require.Empty(t, out)
}

// TestSanitizeAnthropicBodyForBetaTokens_HaikuRealCCClientPreservesField
// supplies a Haiku body with context management and a client-enabled context
// token. The field remains present and unchanged despite the selected model
// name.
func TestSanitizeAnthropicBodyForBetaTokens_HaikuRealCCClientPreservesField(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	// A Claude Code client supplies the context-management beta.
	clientBeta := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, clientBeta)
	require.False(t, changed,
		"真 CC 客户端 header 含 context-management beta 时，haiku body 字段必须保留（功能不丢）")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

// TestSanitizeAnthropicBodyForBetaTokens_NoFallbackFieldsNoChange supplies a
// model and messages with no refusal fallback fields. The result reports no
// changes and retains the request bytes under an OAuth Header.
func TestSanitizeAnthropicBodyForBetaTokens_NoFallbackFieldsNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

// TestSanitizeAnthropicBodyForBetaTokens_FallbacksKeptWhenBetaPresent supplies
// the default refusal fallback value with its server-side token. The field
// retains that value and sanitation reports false rather than replacing the
// request.
func TestSanitizeAnthropicBodyForBetaTokens_FallbacksKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","fallbacks":"default","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body,
		"claude-code-20250219,oauth-2025-04-20,server-side-fallback-2026-07-01")
	require.False(t, changed, "客户端 header 已带 server-side-fallback beta → 字段保留（不过度删除）")
	require.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	require.Equal(t, "default", gjson.GetBytes(out, "fallbacks").String())
}

// TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenBetaMissing
// covers both string and model-array fallback values under unrelated beta
// tokens. Each fallback field is removed while the messages field remains
// available in the result.
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

// TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenHeaderEmpty
// provides a default fallback field and an empty final Header. Sanitation
// reports a deletion and removes that field from the returned request body.
func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenHeaderEmpty(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallbacks":"default","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallbacks").Exists())
}

// TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenStrippedWhenCreditBetaMissing
// supplies a credit token with OAuth and thinking betas alone. The returned
// body drops the credit field and reports a successful sanitation change.
func TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenStrippedWhenCreditBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallback_credit_token").Exists(),
		"缺 credit/fallback beta 时必须 strip fallback_credit_token")
}

// TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenKeptWithAnyAcceptedBeta
// tries each of the three accepted tokens against one credit field. Every
// individual accepted beta keeps the field and leaves the changed flag false.
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

// TestSanitizeAnthropicBodyForBetaTokens_StripsFallbacksKeepsContextManagement
// supplies context management and fallback fields with only the context token.
// It removes fallbacks, retains context management, and reports that at least
// one deletion occurred.
func TestSanitizeAnthropicBodyForBetaTokens_StripsFallbacksKeepsContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body, "context-management-2025-06-27")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
		"header 只有 context-management beta → fallbacks 必须 strip")
	require.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"context-management beta 在 header 中 → context_management 不得被误删")
}

// TestSanitizeAnthropicBodyForBetaTokens_KeepsBothWhenBothBetasPresent supplies
// context management, fallbacks, and a credit field with context and
// server-side tokens. All fields survive and the changed flag remains false.
func TestSanitizeAnthropicBodyForBetaTokens_KeepsBothWhenBothBetasPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := SanitizeAnthropicBodyForBetaTokens(body,
		"context-management-2025-06-27,server-side-fallback-2026-07-01")
	require.False(t, changed, "两个 beta 都在 header 中 → 所有字段保留")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	require.True(t, gjson.GetBytes(out, "fallback_credit_token").Exists())
}

// TestSanitizeAnthropicBodyForBetaTokens_EmptyBodyUnchanged passes nil and an
// empty slice with a fallback-enabling token. Each call returns empty output
// and false because there are no fields to remove.
func TestSanitizeAnthropicBodyForBetaTokens_EmptyBodyUnchanged(t *testing.T) {
	out, changed := SanitizeAnthropicBodyForBetaTokens([]byte{}, "server-side-fallback-2026-07-01")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = SanitizeAnthropicBodyForBetaTokens(nil, "server-side-fallback-2026-07-01")
	require.False(t, changed)
	require.Empty(t, out)
}
