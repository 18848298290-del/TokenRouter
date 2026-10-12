package provider

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

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

func TestThinkingFilters_SkipForPassbackRequired(t *testing.T) {
	in := []byte(passbackThinkingBody)

	require.True(t, bytes.Equal(in, FilterThinkingBlocks(in, "deepseek-v4-pro")))
	require.True(t, bytes.Equal(in, FilterThinkingBlocksForRetry(in, "kimi-k2.6")))
	require.True(t, bytes.Equal(in, FilterSignatureSensitiveBlocksForRetry(in, "glm-5.1")))
}

func TestThinkingFilters_SkipForUnknownModel(t *testing.T) {
	in := []byte(passbackThinkingBody)

	require.True(t, bytes.Equal(in, FilterThinkingBlocks(in, "yi-large")))
	require.True(t, bytes.Equal(in, FilterThinkingBlocksForRetry(in, "gpt-5.5")))
	require.True(t, bytes.Equal(in, FilterSignatureSensitiveBlocksForRetry(in, "")))
}

func TestThinkingFilters_StillStripForAnthropicStrict(t *testing.T) {
	in := []byte(passbackThinkingBody)
	out := FilterThinkingBlocks(in, "claude-sonnet-4-5")

	require.False(t, bytes.Equal(in, out))
	require.NotContains(t, string(out), `"type":"thinking"`)
}

func TestFilterThinkingBlocks(t *testing.T) {
	containsThinkingBlock := func(body []byte) bool {
		var req map[string]any
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
		name         string
		input        string
		shouldFilter bool
		expectError  bool
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

func TestFilterThinkingBlocksForRetry_DropsThinkingBlockWithEmptyContent(t *testing.T) {
	// History from another model may contain a thinking block with empty thinking text.
	// Claude extended thinking rejects it with:
	//   "messages.1.content.0.thinking: each thinking block must contain thinking"
	// Retry cleanup drops the empty block and retains valid content.
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

func TestFilterThinkingBlocksForRetry_StripsEmptyTextBlocks(t *testing.T) {
	// Empty text blocks cause upstream 400 errors: text content blocks must be non-empty.
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

func TestDefaultEffortForThinkingEnabled(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  *string
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

func TestNormalizeChineseLLMThinking(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		input       string
		wantChanged bool
		wantType    string
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

func TestApplyThinkingEnabledFallback(t *testing.T) {
	explicit := "medium"
	tests := []struct {
		name        string
		effort      *string
		body        string
		model       string
		want        *string
		wantSamePtr bool
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

func TestNormalizeGLMOpenAIReasoningEffort(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		input         string
		wantApplied   bool
		wantPath      string
		wantValue     string
		wantUnchanged bool
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

// thinkingStringPointer returns an independent effort value for assertions.
func thinkingStringPointer(value string) *string { return &value }
