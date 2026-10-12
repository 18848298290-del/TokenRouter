package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Replay cleanup keeps a reasoning summary when encrypted_content and content
// are nil. The fixtures also cover removal of empty reasoning items and the
// encrypted versus unencrypted compaction distinction through change flags and
// retained input items.

// TestTrimOpenAIEncryptedReasoningItems_ContentNull surrounds a reasoning summary
// with user and assistant messages. Cleanup retains all three entries while
// removing both nil content and encrypted_content from the reasoning map.
func TestTrimOpenAIEncryptedReasoningItems_ContentNull(t *testing.T) {
	reqBody := map[string]any{
		"model": "grok-4.5",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{
				"type":              "reasoning",
				"summary":           []any{map[string]any{"type": "summary_text", "text": "thinking..."}},
				"content":           nil,
				"encrypted_content": nil,
			},
			map[string]any{"type": "message", "role": "assistant", "content": "Hello!"},
		},
	}

	changed := TrimEncryptedReasoningItems(reqBody)
	require.True(t, changed)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 3)

	reasoning, ok := input[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "reasoning", reasoning["type"])
	assert.NotNil(t, reasoning["summary"])
	_, hasContent := reasoning["content"]
	assert.False(t, hasContent, "content: null should be stripped")
	_, hasEncrypted := reasoning["encrypted_content"]
	assert.False(t, hasEncrypted, "encrypted_content should be stripped")
}

// TestTrimOpenAIEncryptedReasoningItems_ContentNullOnly supplies a summary with
// nil content and no encrypted_content field. Cleanup reports a change, retains
// the one input item, and removes its content key.
func TestTrimOpenAIEncryptedReasoningItems_ContentNullOnly(t *testing.T) {
	reqBody := map[string]any{
		"model": "grok-4.5",
		"input": []any{
			map[string]any{
				"type":    "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": "ok"}},
				"content": nil,
			},
		},
	}

	changed := TrimEncryptedReasoningItems(reqBody)
	require.True(t, changed)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	reasoning, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasContent := reasoning["content"]
	assert.False(t, hasContent, "content: null should be stripped even without encrypted_content")
}

// TestTrimOpenAIEncryptedReasoningItems_ContentNonNull supplies plaintext
// reasoning content without encrypted_content. Cleanup reports no change and
// retains the exact content string available through the input map.
func TestTrimOpenAIEncryptedReasoningItems_ContentNonNull(t *testing.T) {
	reqBody := map[string]any{
		"model": "grok-4.5",
		"input": []any{
			map[string]any{
				"type":    "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": "ok"}},
				"content": "some actual content",
			},
		},
	}

	changed := TrimEncryptedReasoningItems(reqBody)
	assert.False(t, changed, "non-null content should not be stripped")

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	reasoning, ok := input[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "some actual content", reasoning["content"])
}

// TestTrimOpenAIEncryptedReasoningItems_NoReasoningItems supplies one ordinary
// user message. The cleanup change flag stays false for this request, which has
// no reasoning or encrypted compaction item.
func TestTrimOpenAIEncryptedReasoningItems_NoReasoningItems(t *testing.T) {
	reqBody := map[string]any{
		"model": "grok-4.5",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
		},
	}

	changed := TrimEncryptedReasoningItems(reqBody)
	assert.False(t, changed)
}

// TestTrimOpenAIEncryptedReasoningItems_Compaction compares both encrypted
// compaction labels with an unencrypted compaction object. Encrypted items are
// removed ahead of the remaining message; the unencrypted item and two-item
// input retain their type and length.
func TestTrimOpenAIEncryptedReasoningItems_Compaction(t *testing.T) {
	tests := []struct {
		name      string // subtest display name
		itemType  string // supplied compaction label
		encrypted bool   // include ciphertext field
		changed   bool   // expected cleanup flag
	}{
		{name: "compaction", itemType: "compaction", encrypted: true, changed: true},
		{name: "compaction summary", itemType: "compaction_summary", encrypted: true, changed: true},
		{name: "unencrypted compaction", itemType: "compaction", encrypted: false, changed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := map[string]any{"type": tt.itemType, "id": "cmp_stale"}
			if tt.encrypted {
				item["encrypted_content"] = "gAAA"
			}
			reqBody := map[string]any{"input": []any{
				item,
				map[string]any{"type": "message", "content": "hi"},
			}}

			changed := TrimEncryptedReasoningItems(reqBody)
			assert.Equal(t, tt.changed, changed)
			input, ok := reqBody["input"].([]any)
			require.True(t, ok)
			require.NotEmpty(t, input)
			first, ok := input[0].(map[string]any)
			require.True(t, ok)
			if tt.changed {
				require.Len(t, input, 1)
				assert.Equal(t, "message", first["type"])
				return
			}
			require.Len(t, input, 2)
			assert.Equal(t, tt.itemType, first["type"])
		})
	}
}

// TestTrimOpenAIEncryptedReasoningItems_ContentNullDropsBareSkeleton starts with
// a reasoning item containing only type and nil content. Cleanup reports a
// change and removes the input key after dropping the emptied item.
func TestTrimOpenAIEncryptedReasoningItems_ContentNullDropsBareSkeleton(t *testing.T) {
	reqBody := map[string]any{
		"input": []any{
			map[string]any{"type": "reasoning", "content": nil},
		},
	}

	changed := TrimEncryptedReasoningItems(reqBody)
	require.True(t, changed)
	_, hasInput := reqBody["input"]
	assert.False(t, hasInput, "bare reasoning skeleton should be dropped, emptying input")
}
