package provider

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// passbackThinkingBody supplies enabled thinking with unsigned assistant history
// to compare mapped-family passback preservation against Anthropic-oriented filtering rules.
const passbackThinkingBody = `{
	"model":"deepseek-v4-pro",
	"thinking":{"type":"enabled","budget_tokens":1024},
	"messages":[
		{"role":"user","content":[{"type":"text","text":"Hi"}]},
		{"role":"assistant","content":[
			{"type":"thinking","thinking":"Let me think..."},
			{"type":"text","text":"Answer"}
		]}
	]
}`

// TestThinkingFilters_SkipForPassbackRequired uses thinking history with
// passback-required DeepSeek, Kimi, and GLM identities. The selected
// initial/retry/signature filters return the exact supplied bytes.
func TestThinkingFilters_SkipForPassbackRequired(t *testing.T) {
	in := []byte(passbackThinkingBody)

	require.True(t, bytes.Equal(in, FilterThinkingBlocks(in, "deepseek-v4-pro")))
	require.True(t, bytes.Equal(in, FilterThinkingBlocksForRetry(in, "kimi-k2.6")))
	require.True(t, bytes.Equal(in, FilterSignatureSensitiveBlocksForRetry(in, "glm-5.1")))
}

// TestThinkingFilters_SkipForUnknownModel uses thinking history with unknown
// or empty mapped identities supplied explicitly. Those family gates decline
// the tested filters and return unchanged bytes.
func TestThinkingFilters_SkipForUnknownModel(t *testing.T) {
	in := []byte(passbackThinkingBody)

	require.True(t, bytes.Equal(in, FilterThinkingBlocks(in, "yi-large")))
	require.True(t, bytes.Equal(in, FilterThinkingBlocksForRetry(in, "gpt-5.5")))
	require.True(t, bytes.Equal(in, FilterSignatureSensitiveBlocksForRetry(in, "")))
}

// TestThinkingFilters_StillStripForAnthropicStrict uses the passback fixture
// under a strict Claude mapped model. Initial filtering changes the body and
// removes the thinking type marker.
func TestThinkingFilters_StillStripForAnthropicStrict(t *testing.T) {
	in := []byte(passbackThinkingBody)
	out := FilterThinkingBlocks(in, "claude-sonnet-4-5")

	require.False(t, bytes.Equal(in, out))
	require.NotContains(t, string(out), `"type":"thinking"`)
}

// TestFilterThinkingBlocks compares signed/unsigned adaptive history, untyped
// thinking, ordinary tool input, absent messages, and invalid JSON. Selected
// thinking blocks disappear while unchanged cases retain bytes and valid
// rewritten bodies decode.
func TestFilterThinkingBlocks(t *testing.T) {
	containsThinkingBlock := func(body []byte) bool {
		var req map[string]any
		// This predicate supports filtering assertions for decoded message content.
		// Unreadable fixture output returns false here; the test separately
		// validates successful rewritten JSON.
		if err := json.Unmarshal(body, &req); err != nil {
			return false
		}
		messages, ok := req["messages"].([]any)
		if !ok {
			return false
		}
		for _, msg := range messages {
			msgMap, ok := msg.(map[string]any)
			if !ok {
				continue
			}
			content, ok := msgMap["content"].([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				blockMap, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := blockMap["type"].(string)
				if blockType == "thinking" {
					return true
				}
				if blockType == "" {
					if _, hasThinking := blockMap["thinking"]; hasThinking {
						return true
					}
				}
			}
		}
		return false
	}

	tests := []struct {
		name         string // thinking filter scenario label
		input        string // incoming thinking history JSON
		shouldFilter bool   // expected historical thinking removal
		expectError  bool   // malformed body retained case
	}{
		{
			name:         "filters thinking blocks",
			input:        `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":[{"type":"text","text":"Hello"},{"type":"thinking","thinking":"internal","signature":"invalid"},{"type":"text","text":"World"}]}]}`,
			shouldFilter: true,
		},
		{
			name:         "does not filter signed thinking blocks when thinking adaptive",
			input:        `{"thinking":{"type":"adaptive"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"ok","signature":"sig_real_123"},{"type":"text","text":"B"}]}]}`,
			shouldFilter: false,
		},
		{
			name:         "filters unsigned thinking blocks when thinking adaptive",
			input:        `{"thinking":{"type":"adaptive"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"internal","signature":""},{"type":"text","text":"B"}]}]}`,
			shouldFilter: true,
		},
		{
			name:         "handles no thinking blocks",
			input:        `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`,
			shouldFilter: false,
		},
		{
			name:         "handles invalid JSON gracefully",
			input:        `{invalid json`,
			shouldFilter: false,
			expectError:  true,
		},
		{
			name:         "handles multiple messages with thinking blocks",
			input:        `{"messages":[{"role":"user","content":[{"type":"text","text":"A"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"think"},{"type":"text","text":"B"}]}]}`,
			shouldFilter: true,
		},
		{
			name:         "filters thinking blocks without type discriminator",
			input:        `{"messages":[{"role":"assistant","content":[{"thinking":{"text":"internal"}},{"type":"text","text":"B"}]}]}`,
			shouldFilter: true,
		},
		{
			name:         "does not filter tool_use input fields named thinking",
			input:        `{"messages":[{"role":"user","content":[{"type":"tool_use","id":"t1","name":"foo","input":{"thinking":"keepme","x":1}},{"type":"text","text":"Hello"}]}]}`,
			shouldFilter: false,
		},
		{
			name:         "handles empty messages array",
			input:        `{"messages":[]}`,
			shouldFilter: false,
		},
		{
			name:         "handles missing messages field",
			input:        `{"model":"claude-3"}`,
			shouldFilter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterThinkingBlocks([]byte(tt.input))

			if tt.expectError {
				// Invalid JSON passes through unchanged.
				require.Equal(t, tt.input, string(result))
				return
			}

			if tt.shouldFilter {
				require.False(t, containsThinkingBlock(result))
			} else {
				// Unneeded filtering leaves the input unchanged.
				require.Equal(t, tt.input, string(result))
			}

			// Filtered output remains valid JSON.
			var parsed map[string]any
			err := json.Unmarshal(result, &parsed)
			require.NoError(t, err)
		})
	}
}

// TestFilterThinkingBlocksForRetry_DisablesThinkingAndPreservesAsText supplies
// signed thinking plus visible text under enabled mode. Retry cleanup removes
// the control and converts thinking text into a retained text block.
func TestFilterThinkingBlocksForRetry_DisablesThinkingAndPreservesAsText(t *testing.T) {
	input := []byte(`{
		"model":"claude-3-5-sonnet-20241022",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"Let me think...","signature":"bad_sig"},
				{"type":"text","text":"Answer"}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking)

	msgs, ok := req["messages"].([]any)
	require.True(t, ok)
	require.Len(t, msgs, 2)

	assistant, ok := msgs[1].(map[string]any)
	require.True(t, ok)
	content, ok := assistant["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2)

	first, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", first["type"])
	require.Equal(t, "Let me think...", first["text"])
}

// TestFilterThinkingBlocksForRetry_DisablesThinkingEvenWithoutThinkingBlocks
// supplies enabled thinking with a budget and ordinary user/assistant text
// content. Retry cleanup still removes the top-level thinking field when no
// historical thinking blocks exist.
func TestFilterThinkingBlocksForRetry_DisablesThinkingEvenWithoutThinkingBlocks(t *testing.T) {
	input := []byte(`{
		"model":"claude-3-5-sonnet-20241022",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Hi"}]},
			{"role":"assistant","content":[{"type":"text","text":"Prefill"}]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking)
}

// TestFilterThinkingBlocksForRetry_RemovesRedactedThinkingAndKeepsValidContent
// mixes redacted thinking with visible text under enabled mode. Cleanup
// removes the control and redacted block while retaining the visible text.
func TestFilterThinkingBlocksForRetry_RemovesRedactedThinkingAndKeepsValidContent(t *testing.T) {
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"redacted_thinking","data":"..."},
				{"type":"text","text":"Visible"}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking)

	msgs, ok := req["messages"].([]any)
	require.True(t, ok)
	msg0, ok := msgs[0].(map[string]any)
	require.True(t, ok)
	content, ok := msg0["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	content0, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", content0["type"])
	require.Equal(t, "Visible", content0["text"])
}

// TestFilterThinkingBlocksForRetry_DropsThinkingBlockWithEmptyContent supplies
// an empty thinking block before visible answer text. Retry cleanup removes
// the thinking control and empty block, retaining the answer.
func TestFilterThinkingBlocksForRetry_DropsThinkingBlockWithEmptyContent(t *testing.T) {
	// Empty thinking text exercises retry removal while a visible answer remains
	// available. The assertions check the rewritten history and control fields
	// without requiring an upstream exchange.
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":"sig"},
				{"type":"text","text":"Answer"}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking, "top-level thinking should be removed")

	msgs := testassert.MustType[[]any](req["messages"])
	assistant := testassert.MustType[map[string]any](msgs[1])
	content := testassert.MustType[[]any](assistant["content"])
	require.Len(t, content, 1, "empty thinking block should be dropped, only text remains")
	require.Equal(t, "text", testassert.MustType[map[string]any](content[0])["type"])
	require.Equal(t, "Answer", testassert.MustType[map[string]any](content[0])["text"])
}

// TestFilterThinkingBlocksForRetry_EmptyContentGetsPlaceholder supplies a
// message whose content is redacted thinking alone. Retry cleanup creates one
// nonempty text placeholder for the otherwise empty message.
func TestFilterThinkingBlocksForRetry_EmptyContentGetsPlaceholder(t *testing.T) {
	input := []byte(`{
		"thinking":{"type":"enabled"},
		"messages":[
			{"role":"assistant","content":[{"type":"redacted_thinking","data":"..."}]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	msgs, ok := req["messages"].([]any)
	require.True(t, ok)
	msg0, ok := msgs[0].(map[string]any)
	require.True(t, ok)
	content, ok := msg0["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	content0, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", content0["type"])
	require.NotEmpty(t, content0["text"])
}

// TestFilterThinkingBlocksForRetry_StripsEmptyTextBlocks mixes nonempty/empty
// user text with an empty assistant message. Cleanup retains the user text and
// supplies nonempty placeholder content for the assistant.
func TestFilterThinkingBlocksForRetry_StripsEmptyTextBlocks(t *testing.T) {
	// One message has usable text beside an empty block; another has only empty
	// text. Retry cleanup retains the first message's text and supplies the
	// second message with a placeholder.
	input := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":""}]},
			{"role":"assistant","content":[{"type":"text","text":""}]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	msgs, ok := req["messages"].([]any)
	require.True(t, ok)

	// The first message retains hello after removing its empty text block.
	msg0 := testassert.MustType[map[string]any](msgs[0])
	content0 := testassert.MustType[[]any](msg0["content"])
	require.Len(t, content0, 1)
	require.Equal(t, "hello", testassert.MustType[map[string]any](content0[0])["text"])

	// The otherwise empty second message receives placeholder text.
	msg1 := testassert.MustType[map[string]any](msgs[1])
	content1 := testassert.MustType[[]any](msg1["content"])
	require.Len(t, content1, 1)
	block1 := testassert.MustType[map[string]any](content1[0])
	require.Equal(t, "text", block1["type"])
	require.NotEmpty(t, block1["text"])
}

// TestFilterThinkingBlocksForRetry_StripsNestedEmptyTextInToolResult mixes
// valid and empty text inside a tool_result. Cleanup retains the tool result
// and its valid nested text while removing the empty block.
func TestFilterThinkingBlocksForRetry_StripsNestedEmptyTextInToolResult(t *testing.T) {
	// Empty text blocks inside tool_result are removed too.
	input := []byte(`{
		"messages":[
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","content":[
					{"type":"text","text":"valid result"},
					{"type":"text","text":""}
				]}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	msgs := testassert.MustType[[]any](req["messages"])
	msg0 := testassert.MustType[map[string]any](msgs[0])
	content0 := testassert.MustType[[]any](msg0["content"])
	require.Len(t, content0, 1)
	toolResult := testassert.MustType[map[string]any](content0[0])
	require.Equal(t, "tool_result", toolResult["type"])
	nestedContent := testassert.MustType[[]any](toolResult["content"])
	require.Len(t, nestedContent, 1)
	require.Equal(t, "valid result", testassert.MustType[map[string]any](nestedContent[0])["text"])
}

// TestFilterThinkingBlocksForRetry_NestedAllEmptyGetsEmptySlice supplies a
// tool_result containing empty text alongside visible user text. Cleanup
// retains both outer items and returns empty nested result content.
func TestFilterThinkingBlocksForRetry_NestedAllEmptyGetsEmptySlice(t *testing.T) {
	// A tool_result containing only empty text blocks receives an empty content slice.
	input := []byte(`{
		"messages":[
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","content":[
					{"type":"text","text":""}
				]},
				{"type":"text","text":"hello"}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	msgs := testassert.MustType[[]any](req["messages"])
	msg0 := testassert.MustType[map[string]any](msgs[0])
	content0 := testassert.MustType[[]any](msg0["content"])
	require.Len(t, content0, 2)
	toolResult := testassert.MustType[map[string]any](content0[0])
	nestedContent := testassert.MustType[[]any](toolResult["content"])
	require.Len(t, nestedContent, 0)
}

// TestFilterThinkingBlocksForRetry_PreservesNonEmptyTextBlocks supplies
// ordinary nonempty text blocks with no thinking or cleanup trigger. Retry
// filtering returns the exact supplied body bytes.
func TestFilterThinkingBlocksForRetry_PreservesNonEmptyTextBlocks(t *testing.T) {
	// Nonempty text blocks pass through unchanged.
	input := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	// With no thinking or empty text blocks, the input remains unchanged.
	require.Equal(t, input, out)
}

// TestFilterSignatureSensitiveBlocksForRetry_DowngradesTools supplies tool-use
// and result history under enabled thinking. Signature-sensitive cleanup
// removes the control and converts both tool items into text carrying their
// type information.
func TestFilterSignatureSensitiveBlocksForRetry_DowngradesTools(t *testing.T) {
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},
				{"type":"tool_result","tool_use_id":"t1","content":"ok","is_error":false}
			]}
		]
	}`)

	out := FilterSignatureSensitiveBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking)

	msgs, ok := req["messages"].([]any)
	require.True(t, ok)
	msg0, ok := msgs[0].(map[string]any)
	require.True(t, ok)
	content, ok := msg0["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2)
	content0, ok := content[0].(map[string]any)
	require.True(t, ok)
	content1, ok := content[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", content0["type"])
	require.Equal(t, "text", content1["type"])
	require.Contains(t, content0["text"], "tool_use")
	require.Contains(t, content1["text"], "tool_result")
}

// TestFilterThinkingBlocksForRetry_RemovesClearThinkingStrategy_FastPath
// supplies enabled thinking and clear_thinking context edits with ordinary
// messages. Retry cleanup removes thinking and the now-empty edits field.
func TestFilterThinkingBlocksForRetry_RemovesClearThinkingStrategy_FastPath(t *testing.T) {
	// The fast path has top-level thinking but no thinking blocks in messages.
	// It must still remove thinking-dependent context strategies before returning.
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"Hello"}]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking, "顶层 thinking 应被移除")

	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	_, hasEdits := cm["edits"]
	require.False(t, hasEdits, "fast path 下 clear_thinking_20251015 应被移除，edits 键应被删除")
}

// TestFilterThinkingBlocksForRetry_RemovesClearThinkingStrategy_WithThinkingBlocks
// mixes clear_thinking and another edit with thinking history. Cleanup removes
// the control and thinking-specific edit while retaining the unrelated context
// strategy.
func TestFilterThinkingBlocksForRetry_RemovesClearThinkingStrategy_WithThinkingBlocks(t *testing.T) {
	// The full path includes thinking blocks in message history.
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"},{"type":"keep_this"}]},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"some thought","signature":"sig"},
				{"type":"text","text":"Answer"}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking, "顶层 thinking 应被移除")

	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	edits, ok := cm["edits"].([]any)
	require.True(t, ok)
	require.Len(t, edits, 1, "仅移除 clear_thinking_20251015，保留 keep_this")
	edit0, ok := edits[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "keep_this", edit0["type"])
}

// TestFilterThinkingBlocksForRetry_NoContextManagement_Unaffected supplies
// enabled thinking and ordinary user text with no context_management field.
// Cleanup removes the thinking control and leaves the absent
// context-management value uncreated.
func TestFilterThinkingBlocksForRetry_NoContextManagement_Unaffected(t *testing.T) {
	// Missing context_management is accepted while thinking is removed.
	input := []byte(`{
		"thinking":{"type":"enabled"},
		"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]
	}`)

	out := FilterThinkingBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking)
	_, hasCM := req["context_management"]
	require.False(t, hasCM)
}

// TestFilterSignatureSensitiveBlocksForRetry_RemovesClearThinkingStrategy
// supplies signed thinking and a clear_thinking strategy. Signature cleanup
// removes top-level thinking and excludes that strategy from any surviving
// edit list.
func TestFilterSignatureSensitiveBlocksForRetry_RemovesClearThinkingStrategy(t *testing.T) {
	input := []byte(`{
		"thinking":{"type":"enabled","budget_tokens":1024},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"thought","signature":"sig"}
			]}
		]
	}`)

	out := FilterSignatureSensitiveBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	_, hasThinking := req["thinking"]
	require.False(t, hasThinking, "顶层 thinking 应被移除")

	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	if rawEdits, hasEdits := cm["edits"]; hasEdits {
		edits, ok := rawEdits.([]any)
		require.True(t, ok)
		for _, e := range edits {
			em, ok := e.(map[string]any)
			require.True(t, ok)
			require.NotEqual(t, "clear_thinking_20251015", em["type"], "clear_thinking_20251015 应被移除")
		}
	}
}

// TestFilterSignatureSensitiveBlocksForRetry_PreservesNonThinkingStrategies
// mixes a thinking-specific strategy with another edit under enabled mode.
// Signature cleanup retains the unrelated edit as the sole context strategy.
func TestFilterSignatureSensitiveBlocksForRetry_PreservesNonThinkingStrategies(t *testing.T) {
	input := []byte(`{
		"thinking":{"type":"enabled"},
		"context_management":{"edits":[{"type":"clear_thinking_20251015"},{"type":"other_edit"}]},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"t","signature":"s"}
			]}
		]
	}`)

	out := FilterSignatureSensitiveBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))

	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	edits, ok := cm["edits"].([]any)
	require.True(t, ok)
	require.Len(t, edits, 1, "仅移除 clear_thinking_20251015，保留 other_edit")
	edit0, ok := edits[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "other_edit", edit0["type"])
}

// TestFilterSignatureSensitiveBlocksForRetry_NoThinkingField_ContextManagementUntouched
// supplies historical thinking and clear_thinking edits with no top-level
// control. Signature cleanup retains the original context edit list for that
// condition.
func TestFilterSignatureSensitiveBlocksForRetry_NoThinkingField_ContextManagementUntouched(t *testing.T) {
	// Without top-level thinking, context_management remains unchanged.
	input := []byte(`{
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"t","signature":"s"}
			]}
		]
	}`)

	out := FilterSignatureSensitiveBlocksForRetry(input)

	var req map[string]any
	require.NoError(t, json.Unmarshal(out, &req))
	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok)
	edits, ok := cm["edits"].([]any)
	require.True(t, ok)
	require.Len(t, edits, 1, "无顶层 thinking 时 context_management 不应被修改")
}

// TestDefaultEffortForThinkingEnabled compares passback families, native
// DeepSeek effort, Claude, unknown, and empty models. Display defaults return
// high only for the tested eligible passback identities.
func TestDefaultEffortForThinkingEnabled(t *testing.T) {
	tests := []struct {
		name  string  // display effort scenario label
		model string  // mapped model family identity
		want  *string // expected optional display effort
	}{
		{name: "glm", model: "glm-5.1", want: thinkingStringPointer("high")},
		{name: "kimi", model: "kimi-k2.6", want: thinkingStringPointer("high")},
		{name: "moonshot", model: "moonshot-v1-8k", want: thinkingStringPointer("high")},
		{name: "minimax lowercase", model: "minimax-m3", want: thinkingStringPointer("high")},
		{name: "minimax mixed case", model: "MiniMax-M3", want: thinkingStringPointer("high")},
		{name: "qwen thinking", model: "qwen3-235b-a22b-thinking-2507", want: thinkingStringPointer("high")},
		{name: "deepseek excluded", model: "deepseek-v4-pro", want: nil},
		{name: "claude strict", model: "claude-opus-4.6", want: nil},
		{name: "unknown", model: "gpt-5.5", want: nil},
		{name: "empty", model: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultEffortForThinkingEnabled(tt.model)
			if tt.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, *tt.want, *got)
		})
	}
}

// TestNormalizeChineseLLMThinking compares MiniMax enabled/adaptive modes,
// Kimi enabled input, and malformed JSON. Eligible enabled MiniMax converts to
// adaptive, while every declined case retains the exact supplied bytes.
func TestNormalizeChineseLLMThinking(t *testing.T) {
	tests := []struct {
		name        string // thinking transformation scenario label
		model       string // mapped model family identity
		input       string // incoming thinking request JSON
		wantChanged bool   // expected successful body rewrite
		wantType    string // expected outgoing thinking type
	}{
		{
			name:        "minimax enabled becomes adaptive",
			model:       "MiniMax-M3",
			input:       `{"thinking":{"type":"enabled","budget_tokens":8192},"messages":[]}`,
			wantChanged: true,
			wantType:    "adaptive",
		},
		{
			name:     "minimax adaptive unchanged",
			model:    "MiniMax-M2.7",
			input:    `{"thinking":{"type":"adaptive"},"messages":[]}`,
			wantType: "adaptive",
		},
		{
			name:     "kimi unchanged",
			model:    "kimi-k2.6",
			input:    `{"thinking":{"type":"enabled"},"messages":[]}`,
			wantType: "enabled",
		},
		{
			name:  "invalid json unchanged",
			model: "MiniMax-M3",
			input: `{not json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := NormalizeChineseLLMThinking([]byte(tt.input), tt.model)
			require.Equal(t, tt.wantChanged, changed)
			if !tt.wantChanged {
				require.Equal(t, tt.input, string(got))
			}
			if tt.wantType != "" {
				require.Equal(t, tt.wantType, gjson.GetBytes(got, "thinking.type").String())
			}
		})
	}
}

// TestApplyThinkingEnabledFallback compares explicit pointers and family/body
// states with no effort. Existing pointer identity survives, while only the
// tested enabled/adaptive passback cases receive high.
func TestApplyThinkingEnabledFallback(t *testing.T) {
	explicit := "medium"
	tests := []struct {
		name        string  // thinking transformation scenario label
		effort      *string // supplied optional effort pointer
		body        string  // incoming thinking control JSON
		model       string  // mapped model family identity
		want        *string // expected generated effort pointer
		wantSamePtr bool    // supplied pointer should survive
	}{
		{name: "explicit effort unchanged", effort: &explicit, body: `{"thinking":{"type":"enabled"}}`, model: "glm-5.1", wantSamePtr: true},
		{name: "glm thinking enabled defaults high", body: `{"thinking":{"type":"enabled"}}`, model: "glm-5.1", want: thinkingStringPointer("high")},
		{name: "minimax adaptive defaults high", body: `{"thinking":{"type":"adaptive"}}`, model: "MiniMax-M3", want: thinkingStringPointer("high")},
		{name: "deepseek excluded", body: `{"thinking":{"type":"enabled"}}`, model: "deepseek-v4-pro", want: nil},
		{name: "thinking disabled no fallback", body: `{"thinking":{"type":"disabled"}}`, model: "glm-5.1", want: nil},
		{name: "unknown model no fallback", body: `{"thinking":{"type":"enabled"}}`, model: "gpt-5.5", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyThinkingEnabledFallback(tt.effort, []byte(tt.body), tt.model)
			if tt.wantSamePtr {
				require.Same(t, tt.effort, got)
				return
			}
			if tt.want == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, *tt.want, *got)
		})
	}
}

// TestNormalizeGLMOpenAIReasoningEffort supplies GLM effort aliases, GLM 5.3
// low, native max, unrelated family, and absent/unknown values. Converted
// fields follow the expected paths and all declined cases retain bytes.
func TestNormalizeGLMOpenAIReasoningEffort(t *testing.T) {
	tests := []struct {
		name          string // GLM effort normalization case
		model         string // mapped GLM family identity
		input         string // incoming effort request JSON
		wantApplied   bool   // expected effort normalization result
		wantPath      string // outgoing effort field path
		wantValue     string // expected outgoing effort value
		wantUnchanged bool   // expected original byte retention
	}{
		{
			name:        "flat xhigh maps to max",
			model:       "glm-5.2",
			input:       `{"model":"glm-5.2","reasoning_effort":"xhigh","messages":[]}`,
			wantApplied: true,
			wantPath:    "reasoning_effort",
			wantValue:   "max",
		},
		{
			name:        "flat x-high maps to max",
			model:       "GLM-5.2",
			input:       `{"model":"glm-5.2","reasoning_effort":"x-high","messages":[]}`,
			wantApplied: true,
			wantPath:    "reasoning_effort",
			wantValue:   "max",
		},
		{
			name:        "flat ultracode maps to max",
			model:       "glm-5.2",
			input:       `{"model":"glm-5.2","reasoning_effort":"ultracode","messages":[]}`,
			wantApplied: true,
			wantPath:    "reasoning_effort",
			wantValue:   "max",
		},
		{
			name:        "flat medium maps to high",
			model:       "glm-5.2",
			input:       `{"model":"glm-5.2","reasoning_effort":"medium","messages":[]}`,
			wantApplied: true,
			wantPath:    "reasoning_effort",
			wantValue:   "high",
		},
		{
			name:        "glm 5.2 low maps to high",
			model:       "glm-5.2",
			input:       `{"model":"glm-5.2","reasoning_effort":"low","messages":[]}`,
			wantApplied: true,
			wantPath:    "reasoning_effort",
			wantValue:   "high",
		},
		{
			name:        "nested high case-normalizes",
			model:       "glm-5.2",
			input:       `{"model":"glm-5.2","reasoning":{"effort":"HIGH"},"messages":[]}`,
			wantApplied: true,
			wantPath:    "reasoning.effort",
			wantValue:   "high",
		},
		{
			name:          "glm 5.3 native low unchanged",
			model:         "glm-5.3",
			input:         `{"model":"glm-5.3","reasoning_effort":"low","messages":[]}`,
			wantApplied:   false,
			wantUnchanged: true,
		},
		{
			name:          "native max unchanged",
			model:         "glm-5.2",
			input:         `{"model":"glm-5.2","reasoning_effort":"max","messages":[]}`,
			wantApplied:   false,
			wantUnchanged: true,
		},
		{
			name:          "non glm unchanged",
			model:         "deepseek-v4-pro",
			input:         `{"model":"deepseek-v4-pro","reasoning_effort":"xhigh","messages":[]}`,
			wantApplied:   false,
			wantUnchanged: true,
		},
		{
			name:          "missing effort unchanged",
			model:         "glm-5.2",
			input:         `{"model":"glm-5.2","messages":[]}`,
			wantApplied:   false,
			wantUnchanged: true,
		},
		{
			name:          "unknown effort unchanged",
			model:         "glm-5.2",
			input:         `{"model":"glm-5.2","reasoning_effort":"banana","messages":[]}`,
			wantApplied:   false,
			wantUnchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, applied := NormalizeGLMOpenAIReasoningEffort([]byte(tt.input), tt.model)
			require.Equal(t, tt.wantApplied, applied)
			if tt.wantUnchanged {
				require.Equal(t, tt.input, string(got))
				return
			}
			require.Equal(t, tt.wantValue, gjson.GetBytes(got, tt.wantPath).String())
		})
	}
}

// thinkingStringPointer returns separately allocated effort pointer storage
// for test assertions.
func thinkingStringPointer(value string) *string { return &value }
