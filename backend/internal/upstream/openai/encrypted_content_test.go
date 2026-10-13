package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCollectOpenAIEncryptedContentDigestsRaw mixes supported encrypted items,
// plaintext input, missing ciphertext, and an unsupported message type. It
// checks digest order and that unsupported or absent input contributes no
// tracked ciphertext.
func TestCollectOpenAIEncryptedContentDigestsRaw(t *testing.T) {
	t.Parallel()

	digests := CollectOpenAIEncryptedContentDigestsRaw([]byte(`{"input":[
		{"type":"reasoning","encrypted_content":"cipher-a"},
		{"type":"compaction","encrypted_content":"cipher-b"},
		{"type":"input_text","text":"hello"},
		{"type":"reasoning"},
		{"type":"message","encrypted_content":"cipher-alien"}
	]}`))
	// Unsupported message items must not contribute digests that the sanitizer cannot remove.
	require.Equal(t, []string{
		OpenAIEncryptedContentDigest("cipher-a"),
		OpenAIEncryptedContentDigest("cipher-b"),
	}, digests)

	require.Nil(t, CollectOpenAIEncryptedContentDigestsRaw([]byte(`{"model":"gpt-5"}`)))
}

// TestStripOpenAIInvalidEncryptedContentItems supplies stale and fresh
// ciphertext in decoded histories. It protects reasoning-shell retention,
// encrypted compaction removal, fresh-item survival, and zero changes when no
// digest matches.
func TestStripOpenAIInvalidEncryptedContentItems(t *testing.T) {
	t.Parallel()

	invalid := map[string]struct{}{
		OpenAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("only_strips_matching_items", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "stale-cipher", "summary": []any{}},
				map[string]any{"type": "reasoning", "id": "rs_2", "encrypted_content": "fresh-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := StripOpenAIInvalidEncryptedContentItems(reqBody, invalid)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 3)
		first, ok := input[0].(map[string]any)
		require.True(t, ok)
		_, hasEncrypted := first["encrypted_content"]
		require.False(t, hasEncrypted, "命中项应剥离 encrypted_content")
		require.Equal(t, "rs_1", first["id"], "reasoning 骨架应保留")
		second, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "fresh-cipher", second["encrypted_content"], "新密文不得误删")
	})

	t.Run("compaction_removed_entirely", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "compaction", "encrypted_content": "stale-cipher"},
				map[string]any{"type": "input_text", "text": "hello"},
			},
		}
		stripped := StripOpenAIInvalidEncryptedContentItems(reqBody, invalid)
		require.Equal(t, 1, stripped)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		survivor, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "input_text", survivor["type"])
	})

	t.Run("no_hit_no_change", func(t *testing.T) {
		t.Parallel()
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"type": "reasoning", "encrypted_content": "fresh-cipher"},
			},
		}
		require.Zero(t, StripOpenAIInvalidEncryptedContentItems(reqBody, invalid))
	})
}

// TestStripOpenAIInvalidEncryptedContentFromReplayItems sanitizes stale
// reasoning and compaction alongside unchanged raw input. It checks replacement
// content, removal counts, shared unmatched byte storage, immutable original
// bytes, and no-hit storage reuse.
func TestStripOpenAIInvalidEncryptedContentFromReplayItems(t *testing.T) {
	t.Parallel()

	invalid := map[string]struct{}{
		OpenAIEncryptedContentDigest("stale-cipher"): {},
	}

	t.Run("rewrites_hit_items_and_shares_the_rest", func(t *testing.T) {
		t.Parallel()
		original := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"}`)
		clean := json.RawMessage(`{"type":"input_text","text":"hello"}`)
		compaction := json.RawMessage(`{"type":"compaction","encrypted_content":"stale-cipher"}`)
		items := []json.RawMessage{original, clean, compaction}

		next, stripped := StripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid)
		require.Equal(t, 2, stripped)
		require.Len(t, next, 2, "compaction 命中项应整项删除")
		var rewritten map[string]any
		require.NoError(t, json.Unmarshal(next[0], &rewritten))
		require.Equal(t, "rs_1", rewritten["id"])
		_, hasEncrypted := rewritten["encrypted_content"]
		require.False(t, hasEncrypted)
		// Unmatched items share their bodies; changed items receive new bodies.
		require.Same(t, &clean[0], &next[1][0])
		require.Contains(t, string(original), "stale-cipher")
	})

	t.Run("miss_returns_original_header", func(t *testing.T) {
		t.Parallel()
		items := []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"fresh-cipher"}`)}
		next, stripped := StripOpenAIInvalidEncryptedContentFromReplayItems(items, invalid)
		require.Zero(t, stripped)
		require.Same(t, &items[0][0], &next[0][0])
	})
}

// TestStripOpenAIInvalidEncryptedContentRaw rewrites one stale item in a
// complete raw request with fresh history. It checks preserved model and input
// content plus reuse of the original payload storage on a no-hit path.
func TestStripOpenAIInvalidEncryptedContentRaw(t *testing.T) {
	t.Parallel()

	invalid := map[string]struct{}{
		OpenAIEncryptedContentDigest("stale-cipher"): {},
	}
	payload := []byte(`{"model":"gpt-5","input":[
		{"type":"reasoning","id":"rs_1","encrypted_content":"stale-cipher"},
		{"type":"reasoning","id":"rs_2","encrypted_content":"fresh-cipher"},
		{"type":"input_text","text":"hello"}
	],"stream":true}`)

	stripped, count, err := StripOpenAIInvalidEncryptedContentRaw(payload, invalid)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stripped, &decoded))
	require.Equal(t, "gpt-5", decoded["model"])
	input, ok := decoded["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 3)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasEncrypted := first["encrypted_content"]
	require.False(t, hasEncrypted)
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fresh-cipher", second["encrypted_content"])

	t.Run("miss_fast_path_returns_original", func(t *testing.T) {
		t.Parallel()
		cleanPayload := []byte(`{"input":[{"type":"reasoning","encrypted_content":"fresh-cipher"}]}`)
		unchanged, count, err := StripOpenAIInvalidEncryptedContentRaw(cleanPayload, invalid)
		require.NoError(t, err)
		require.Zero(t, count)
		require.Same(t, &cleanPayload[0], &unchanged[0], "未命中时应原样返回，不重编码")
	})
}
