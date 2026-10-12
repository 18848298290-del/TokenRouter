package openaiattempt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// kiroReasoningCanonicalBody represents a Responses request with Kiro-private encrypted reasoning.
const kiroReasoningCanonicalBody = `{"model":"gpt-5.1","stream":false,"input":[` +
	`{"type":"message","role":"user","content":"hello"},` +
	`{"type":"reasoning","id":"rs_kiro_abc123","encrypted_content":"ENC_BLOB","summary":[{"type":"summary_text","text":"thinking"}]},` +
	`{"type":"message","role":"assistant","content":"hi"}` +
	`]}`

func newOpenAIPassthroughProvider(id int64, passthrough bool) *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation, ID: id,
			Platform: capability.PlatformOpenAI,
			Type:     capability.ProviderTypeAPIKey,
			Extra:    map[string]any{"openai_passthrough": passthrough},
		},
	}
}

func reasoningItemCount(t *testing.T, body []byte) int {
	t.Helper()
	count := 0
	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "reasoning" {
			count++
		}
		return true
	})
	return count
}

// TestDeriveOpenAIForwardAttemptBody_CrossModeStripsKiroReasoning checks removal of encrypted reasoning items
// when switching from passthrough to non-passthrough, with canonical input unchanged.
func TestDeriveOpenAIForwardAttemptBody_CrossModeStripsKiroReasoning(t *testing.T) {
	canonical := []byte(kiroReasoningCanonicalBody)
	kiro := newOpenAIPassthroughProvider(1, true)
	bedrock := newOpenAIPassthroughProvider(2, false)
	state := &openAIPassthroughFailoverState{}

	firstBody := deriveOpenAIForwardAttemptBody(nil, canonical, kiro, state)
	require.Equal(t, 1, reasoningItemCount(t, firstBody), "首次透传尝试必须保留 reasoning")
	require.Equal(t, "ENC_BLOB", gjson.GetBytes(firstBody, "input.1.encrypted_content").String())
	require.Equal(t, "rs_kiro_abc123", gjson.GetBytes(firstBody, "input.1.id").String())
	require.JSONEq(t, kiroReasoningCanonicalBody, string(firstBody))

	secondBody := deriveOpenAIForwardAttemptBody(nil, canonical, bedrock, state)
	require.Equal(t, 0, reasoningItemCount(t, secondBody), "跨模式尝试必须删除整个 reasoning 项")
	require.False(t, gjson.GetBytes(secondBody, "input.#(encrypted_content)").Exists())
	require.NotContains(t, string(secondBody), "rs_kiro_abc123")
	require.NotContains(t, string(secondBody), "ENC_BLOB")
	require.Equal(t, 2, int(gjson.GetBytes(secondBody, "input.#").Int()))
	require.Equal(t, "hello", gjson.GetBytes(secondBody, "input.0.content").String())
	require.Equal(t, "hi", gjson.GetBytes(secondBody, "input.1.content").String())
	require.JSONEq(t, kiroReasoningCanonicalBody, string(canonical))
}

// TestDeriveOpenAIForwardAttemptBody_SameModePreservesReasoning checks full canonical reasoning for
// same-mode retries and switches to passthrough providers.
func TestDeriveOpenAIForwardAttemptBody_SameModePreservesReasoning(t *testing.T) {
	canonical := []byte(kiroReasoningCanonicalBody)

	t.Run("non_passthrough_to_non_passthrough", func(t *testing.T) {
		state := &openAIPassthroughFailoverState{}
		first := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(10, false), state)
		second := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(11, false), state)
		require.Equal(t, 1, reasoningItemCount(t, first))
		require.Equal(t, 1, reasoningItemCount(t, second))
		require.JSONEq(t, kiroReasoningCanonicalBody, string(second))
	})

	t.Run("passthrough_to_passthrough", func(t *testing.T) {
		state := &openAIPassthroughFailoverState{}
		first := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(20, true), state)
		second := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(21, true), state)
		require.Equal(t, 1, reasoningItemCount(t, first))
		require.Equal(t, 1, reasoningItemCount(t, second))
	})

	t.Run("non_passthrough_to_passthrough", func(t *testing.T) {
		state := &openAIPassthroughFailoverState{}
		_ = deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(30, false), state)
		second := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(31, true), state)
		require.Equal(t, 1, reasoningItemCount(t, second))
	})

	t.Run("same_provider_pool_retry", func(t *testing.T) {
		state := &openAIPassthroughFailoverState{}
		kiro := newOpenAIPassthroughProvider(40, true)
		_ = deriveOpenAIForwardAttemptBody(nil, canonical, kiro, state)
		retry := deriveOpenAIForwardAttemptBody(nil, canonical, kiro, state)
		require.Equal(t, 1, reasoningItemCount(t, retry))
	})
}

// TestDeriveOpenAIForwardAttemptBody_SanitizationSticksAcrossBedrockRetries checks that
// every subsequent non-passthrough retry uses sanitized input after a cross-mode switch.
func TestDeriveOpenAIForwardAttemptBody_SanitizationSticksAcrossBedrockRetries(t *testing.T) {
	canonical := []byte(kiroReasoningCanonicalBody)
	state := &openAIPassthroughFailoverState{}

	first := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(50, true), state)
	second := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(51, false), state)
	retry := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(51, false), state)
	nextProvider := deriveOpenAIForwardAttemptBody(nil, canonical, newOpenAIPassthroughProvider(52, false), state)

	require.Equal(t, 1, reasoningItemCount(t, first))
	require.Equal(t, 0, reasoningItemCount(t, second))
	require.Equal(t, 0, reasoningItemCount(t, retry))
	require.Equal(t, 0, reasoningItemCount(t, nextProvider))
	require.JSONEq(t, kiroReasoningCanonicalBody, string(canonical))
}
