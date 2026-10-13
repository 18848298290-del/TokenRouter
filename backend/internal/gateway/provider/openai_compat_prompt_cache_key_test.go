package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	protocolanthropic "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// mustRawJSON wraps the supplied fixture bytes as json.RawMessage and marks
// the caller helper. It does not validate JSON, so tests supply the intended
// encoded content directly.
func mustRawJSON(t *testing.T, s string) json.RawMessage {
	t.Helper()
	return json.RawMessage(s)
}

// TestShouldAutoInjectPromptCacheKeyForCompat checks listed GPT-5 and Codex
// model identities against an ordinary GPT-4o name. Selected compatibility
// families qualify for automatic cache identity while that ordinary model is
// excluded.
func TestShouldAutoInjectPromptCacheKeyForCompat(t *testing.T) {
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.5"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.5-pro"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.4"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.4-mini"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.2"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.3"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.3-codex"))
	require.True(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-5.3-codex-spark"))
	require.False(t, ShouldAutoInjectPromptCacheKeyForCompat("gpt-4o"))
}

// TestShouldAutoInjectPromptCacheKeyForCompat_GPT6AstraForms compares exact
// Astra with vendor prefixes, alternate spellings, version suffixes, and other
// families. Only the exact listed Astra identity qualifies among those forms.
func TestShouldAutoInjectPromptCacheKeyForCompat_GPT6AstraForms(t *testing.T) {
	for _, model := range []string{
		"gpt-6-astra",
		"openai/gpt-6-astra",
		"OPENAI/GPT-6_ASTRA",
		"provider/gpt-6-astra",
	} {
		require.Equal(t, model == "gpt-6-astra", ShouldAutoInjectPromptCacheKeyForCompat(model), model)
	}

	for _, model := range []string{
		"gpt-6-terra",
		"gpt-6.1",
		"gpt-6-astra-preview",
		"gpt-6-astra-unrelated-name",
		"gpt-6-astra-2026-09-01",
		"gpt-6-astra-2026-09-01-extra",
		"gpt-6-astra-v",
		"gpt-6-astra-v1.beta",
		"claude-sonnet-4-5",
		"gpt-4o",
	} {
		require.False(t, ShouldAutoInjectPromptCacheKeyForCompat(model), model)
	}
}

// TestDeriveCompatPromptCacheKey_StableAcrossLaterTurns extends a Chat request
// after its system and first user messages. Both requests produce the same
// nonempty cache key for the supplied mapped model.
func TestDeriveCompatPromptCacheKey_StableAcrossLaterTurns(t *testing.T) {
	base := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []protocolopenai.ChatMessage{
			{Role: "system", Content: mustRawJSON(t, `"You are helpful."`)},
			{Role: "user", Content: mustRawJSON(t, `"Hello"`)},
		},
	}
	extended := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []protocolopenai.ChatMessage{
			{Role: "system", Content: mustRawJSON(t, `"You are helpful."`)},
			{Role: "user", Content: mustRawJSON(t, `"Hello"`)},
			{Role: "assistant", Content: mustRawJSON(t, `"Hi there!"`)},
			{Role: "user", Content: mustRawJSON(t, `"How are you?"`)},
		},
	}

	k1 := DeriveCompatPromptCacheKey(base, "gpt-5.4")
	k2 := DeriveCompatPromptCacheKey(extended, "gpt-5.4")
	require.Equal(t, k1, k2, "cache key should be stable across later turns")
	require.NotEmpty(t, k1)
}

// TestDeriveCompatPromptCacheKey_DiffersAcrossSessions creates two requests
// differing in first user content under the same model. The derived
// compatibility keys differ for those opening messages.
func TestDeriveCompatPromptCacheKey_DiffersAcrossSessions(t *testing.T) {
	req1 := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: mustRawJSON(t, `"Question A"`)},
		},
	}
	req2 := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-5.4",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: mustRawJSON(t, `"Question B"`)},
		},
	}

	k1 := DeriveCompatPromptCacheKey(req1, "gpt-5.4")
	k2 := DeriveCompatPromptCacheKey(req2, "gpt-5.4")
	require.NotEqual(t, k1, k2, "different first user messages should yield different keys")
}

// TestDeriveCompatPromptCacheKey_UsesResolvedSparkFamily derives keys for a
// plain Spark model and its padded vendor-prefixed identity. Both produce
// usable keys, and the different full mapped names produce different
// identities.
func TestDeriveCompatPromptCacheKey_UsesResolvedSparkFamily(t *testing.T) {
	req := &protocolopenai.ChatCompletionsRequest{
		Model: "gpt-5.3-codex-spark",
		Messages: []protocolopenai.ChatMessage{
			{Role: "user", Content: mustRawJSON(t, `"Question A"`)},
		},
	}

	k1 := DeriveCompatPromptCacheKey(req, "gpt-5.3-codex-spark")
	k2 := DeriveCompatPromptCacheKey(req, " openai/gpt-5.3-codex-spark ")
	require.NotEmpty(t, k1)
	require.NotEqual(t, k1, k2, "不同完整型号不能共享模型缓存身份")
}

// TestDeriveAnthropicCompatPromptCacheKey_StableAcrossLaterTurns extends an
// Anthropic request after fixed system and first user content. Ordinary
// compatibility derivation retains the same nonempty key despite appended
// later conversation turns.
func TestDeriveAnthropicCompatPromptCacheKey_StableAcrossLaterTurns(t *testing.T) {
	base := &protocolanthropic.AnthropicRequest{
		Model:  "claude-sonnet-4-5",
		System: mustRawJSON(t, `"You are helpful."`),
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: mustRawJSON(t, `"Open repo"`)},
		},
	}
	extended := &protocolanthropic.AnthropicRequest{
		Model:  "claude-sonnet-4-5",
		System: mustRawJSON(t, `"You are helpful."`),
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: mustRawJSON(t, `"Open repo"`)},
			{Role: "assistant", Content: mustRawJSON(t, `"Opened."`)},
			{Role: "user", Content: mustRawJSON(t, `"Run tests"`)},
		},
	}

	k1 := DeriveAnthropicCompatPromptCacheKey(base, "gpt-5.3-codex")
	k2 := DeriveAnthropicCompatPromptCacheKey(extended, "gpt-5.3-codex")
	require.NotEmpty(t, k1)
	require.Equal(t, k1, k2, "cache key should stay stable as later Claude Code turns append history")
}

// TestDeriveAnthropicCompatPromptCacheKey_UsesCacheControlAnchors uses
// ephemeral system/user text anchors and appends unanchored history. The key
// stays stable under anthropic-cache and differs from the ordinary
// compatibility prefix.
func TestDeriveAnthropicCompatPromptCacheKey_UsesCacheControlAnchors(t *testing.T) {
	base := &protocolanthropic.AnthropicRequest{
		Model: "claude-sonnet-4-5",
		System: mustRawJSON(t, `[
			{"type":"text","text":"project instructions","cache_control":{"type":"ephemeral"}}
		]`),
		Messages: []protocolanthropic.AnthropicMessage{
			{Role: "user", Content: mustRawJSON(t, `[
				{"type":"text","text":"repo anchor","cache_control":{"type":"ephemeral"}}
			]`)},
		},
	}
	extended := &protocolanthropic.AnthropicRequest{
		Model:  base.Model,
		System: base.System,
		Messages: []protocolanthropic.AnthropicMessage{
			base.Messages[0],
			{Role: "assistant", Content: mustRawJSON(t, `[{"type":"text","text":"Opened."}]`)},
			{Role: "user", Content: mustRawJSON(t, `[{"type":"text","text":"Run tests"}]`)},
		},
	}

	k1 := DeriveAnthropicCompatPromptCacheKey(base, "gpt-5.4")
	k2 := DeriveAnthropicCompatPromptCacheKey(extended, "gpt-5.4")
	require.NotEmpty(t, k1)
	require.Equal(t, k1, k2)
	require.True(t, strings.HasPrefix(k1, "anthropic-cache-"))
	require.False(t, strings.HasPrefix(k1, compatPromptCacheKeyPrefix))
}
