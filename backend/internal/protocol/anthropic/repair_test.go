package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// TestStripEmptyTextBlocks removes empty text at message and nested tool-result
// levels. Nonempty text and string-valued results must remain usable, while a
// request needing no cleanup retains its supplied bytes.
func TestStripEmptyTextBlocks(t *testing.T) {
	t.Run("strips top-level empty text", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":""}]}]}`)
		out := StripEmptyTextBlocks(input)
		var req map[string]any
		require.NoError(t, json.Unmarshal(out, &req))
		msgs := testassert.MustType[[]any](req["messages"])
		content := testassert.MustType[[]any](testassert.MustType[map[string]any](msgs[0])["content"])
		require.Len(t, content, 1)
		require.Equal(t, "hello", testassert.MustType[map[string]any](content[0])["text"])
	})

	t.Run("strips nested empty text in tool_result", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok"},{"type":"text","text":""}]}]}]}`)
		out := StripEmptyTextBlocks(input)
		var req map[string]any
		require.NoError(t, json.Unmarshal(out, &req))
		msgs := testassert.MustType[[]any](req["messages"])
		content := testassert.MustType[[]any](testassert.MustType[map[string]any](msgs[0])["content"])
		toolResult := testassert.MustType[map[string]any](content[0])
		nestedContent := testassert.MustType[[]any](toolResult["content"])
		require.Len(t, nestedContent, 1)
		require.Equal(t, "ok", testassert.MustType[map[string]any](nestedContent[0])["text"])
	})

	t.Run("no-op when no empty text", func(t *testing.T) {
		input := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
		out := StripEmptyTextBlocks(input)
		require.Equal(t, input, out)
	})

	t.Run("preserves non-map blocks in content", func(t *testing.T) {
		// String-valued tool_result content passes through unchanged.
		input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"string content"},{"type":"text","text":""}]}]}`)
		out := StripEmptyTextBlocks(input)
		var req map[string]any
		require.NoError(t, json.Unmarshal(out, &req))
		msgs := testassert.MustType[[]any](req["messages"])
		content := testassert.MustType[[]any](testassert.MustType[map[string]any](msgs[0])["content"])
		require.Len(t, content, 1)
		toolResult := testassert.MustType[map[string]any](content[0])
		require.Equal(t, "tool_result", toolResult["type"])
		require.Equal(t, "string content", toolResult["content"])
	})

	t.Run("handles deeply nested tool_result", func(t *testing.T) {
		// Empty text is removed from nested tool_result content too.
		input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":""},{"type":"text","text":"deep"}]}]}]}]}`)
		out := StripEmptyTextBlocks(input)
		var req map[string]any
		require.NoError(t, json.Unmarshal(out, &req))
		msgs := testassert.MustType[[]any](req["messages"])
		content := testassert.MustType[[]any](testassert.MustType[map[string]any](msgs[0])["content"])
		outer := testassert.MustType[map[string]any](content[0])
		innerContent := testassert.MustType[[]any](outer["content"])
		inner := testassert.MustType[map[string]any](innerContent[0])
		deepContent := testassert.MustType[[]any](inner["content"])
		require.Len(t, deepContent, 1)
		require.Equal(t, "deep", testassert.MustType[map[string]any](deepContent[0])["text"])
	})
}

// TestRemoveThinkingDependentContextStrategies_NoContextManagement supplies a
// request with thinking but no context-management configuration. Strategy cleanup
// must return the supplied bytes because there is no edits list to repair.
func TestRemoveThinkingDependentContextStrategies_NoContextManagement(t *testing.T) {
	input := []byte(`{"thinking":{"type":"enabled"},"messages":[]}`)
	out := RemoveThinkingDependentContextStrategies(input)
	require.Equal(t, input, out, "无 context_management 字段时应原样返回")
}

// TestRemoveThinkingDependentContextStrategies_EmptyEdits supplies an existing
// empty context edits array. Cleanup must leave that request byte-for-byte
// unchanged because it contains no thinking-dependent strategy to remove.
func TestRemoveThinkingDependentContextStrategies_EmptyEdits(t *testing.T) {
	input := []byte(`{"context_management":{"edits":[]},"messages":[]}`)
	out := RemoveThinkingDependentContextStrategies(input)
	require.Equal(t, input, out, "edits 为空数组时应原样返回")
}

// TestRemoveThinkingDependentContextStrategies_NoClearThinkingEntry supplies a
// context edits list containing another strategy. Cleanup must retain the supplied
// bytes because the list has no clear-thinking entry.
func TestRemoveThinkingDependentContextStrategies_NoClearThinkingEntry(t *testing.T) {
	input := []byte(`{"context_management":{"edits":[{"type":"other_strategy"}]},"messages":[]}`)
	out := RemoveThinkingDependentContextStrategies(input)
	require.Equal(t, input, out, "edits 中无 clear_thinking_20251015 时应原样返回")
}

// TestRemoveThinkingDependentContextStrategies_RemovesSingleEntry removes the
// only clear-thinking edit from a context object. The edits key must disappear
// while the surrounding context_management object remains present.
func TestRemoveThinkingDependentContextStrategies_RemovesSingleEntry(t *testing.T) {
	input := []byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out := RemoveThinkingDependentContextStrategies(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	_, hasEdits := cm["edits"]
	require.False(t, hasEdits, "所有 edits 均为 clear_thinking_20251015 时应删除 edits 键")
}

// TestRemoveThinkingDependentContextStrategies_MixedEntries places a dependent
// clear-thinking edit beside another context strategy. Cleanup must remove the
// dependent entry and retain the other strategy as the sole remaining edit.
func TestRemoveThinkingDependentContextStrategies_MixedEntries(t *testing.T) {
	input := []byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015"},{"type":"other_strategy","param":1}]},"messages":[]}`)
	out := RemoveThinkingDependentContextStrategies(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	edits, ok := cm["edits"].([]any)
	require.True(t, ok)
	require.Len(t, edits, 1, "仅移除 clear_thinking_20251015，保留其他条目")
	edit0, ok := edits[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "other_strategy", edit0["type"])
}
