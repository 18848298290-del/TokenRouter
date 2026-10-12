package requeststate

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestOpenAIBodyHasThinkingEnabled supplies enabled/adaptive case variants,
// missing values, invalid JSON and empty input. The byte predicate must retain
// its stated lenient classification for each actual body fixture.
func TestOpenAIBodyHasThinkingEnabled(t *testing.T) {
	tests := []struct {
		name string // case label for diagnostics
		body string // JSON request fixture text
		want bool   // expected enabled-thinking classification
	}{
		{name: "enabled", body: `{"thinking":{"type":"enabled"}}`, want: true},
		{name: "adaptive", body: `{"thinking":{"type":"adaptive"}}`, want: true},
		{name: "uppercase", body: `{"thinking":{"type":"ENABLED"}}`, want: true},
		{name: "disabled", body: `{"thinking":{"type":"disabled"}}`, want: false},
		{name: "missing type", body: `{"thinking":{"budget_tokens":1024}}`, want: false},
		{name: "missing thinking", body: `{"model":"glm-5.1"}`, want: false},
		{name: "invalid json", body: `{not json`, want: false},
		{name: "empty body", body: ``, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, OpenAIBodyHasThinkingEnabled([]byte(tt.body)))
		})
	}
}

// TestThinkingPolicyPreservesExcludedPayload keeps signed-history filtering
// gates disabled, then enables prefiltering on missing-signature history.
// Disabled gates must return supplied content, while the selected protocol
// filter changes that history.
func TestThinkingPolicyPreservesExcludedPayload(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"history"}]}]}`)
	require.Equal(t, body, FilterThinkingBlocks(body, ThinkingRequestOptions{}))
	require.Equal(t, body, FilterThinkingBlocksForRetry(body, ThinkingRequestOptions{}))
	require.Equal(t, body, FilterSignatureSensitiveBlocksForRetry(body, ThinkingRequestOptions{}))
	filtered := FilterThinkingBlocks(body, ThinkingRequestOptions{PreFilter: true, DummySignature: "placeholder"})
	require.NotEqual(t, body, filtered)
}

// TestThinkingPolicyExplicitEffortAndNativeFallback combines explicit pointer
// evidence, adaptive thinking and native-effort flags. A supplied pointer must
// be retained, native support must omit the reporting default, and selected
// passback must report high.
func TestThinkingPolicyExplicitEffortAndNativeFallback(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive"}}`)
	effort := "max"
	require.Same(t, &effort, ApplyThinkingEnabledFallback(&effort, body, ThinkingRequestOptions{PassbackRequired: true}))
	require.Nil(t, ApplyThinkingEnabledFallback(nil, body, ThinkingRequestOptions{PassbackRequired: true, NativeReasoningEffort: true}))
	require.Equal(t, "high", *ApplyThinkingEnabledFallback(nil, body, ThinkingRequestOptions{PassbackRequired: true}))
}

// TestThinkingPolicyGLMVariants compares the generic GLM low mapping with GLM53
// and normalizes Anthropic/ MiniMax thinking fields. Assertions must retain the
// variant-specific levels and successful enabled/adaptive wire transformations.
func TestThinkingPolicyGLMVariants(t *testing.T) {
	for _, tc := range []struct {
		name    string                 // case label for diagnostics
		options ThinkingRequestOptions // mapped-model thinking policy fixture
		value   string                 // expected family-normalized effort spelling
	}{
		{"legacy", ThinkingRequestOptions{GLM: true}, "high"},
		{"53", ThinkingRequestOptions{GLM: true, GLM53: true}, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := NormalizeGLMOpenAIReasoningEffort([]byte(`{"reasoning_effort":"low"}`), tc.options)
			require.Equal(t, tc.value, gjson.GetBytes(out, "reasoning_effort").String())
		})
	}
	out, changed := NormalizeGLM53AnthropicThinking([]byte(`{"output_config":{"effort":"ultra"}}`), ThinkingRequestOptions{GLM53: true})
	require.True(t, changed)
	require.Equal(t, "enabled", gjson.GetBytes(out, "thinking.type").String())
	require.Equal(t, "max", gjson.GetBytes(out, "output_config.effort").String())
	out, changed = NormalizeChineseLLMThinking([]byte(`{"thinking":{"type":"enabled"}}`), true)
	require.True(t, changed)
	require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
}
