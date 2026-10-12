package upstream

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsolateOpenAISessionID(t *testing.T) {
	t.Run("empty_raw_returns_empty", func(t *testing.T) {
		assert.Equal(t, "", IsolateSessionID(1, ""))
		assert.Equal(t, "", IsolateSessionID(1, "   "))
	})

	t.Run("deterministic", func(t *testing.T) {
		a := IsolateSessionID(42, "sess_abc123")
		b := IsolateSessionID(42, "sess_abc123")
		assert.Equal(t, a, b)
	})

	t.Run("different_apiKeyID_different_result", func(t *testing.T) {
		a := IsolateSessionID(1, "same_session")
		b := IsolateSessionID(2, "same_session")
		require.NotEqual(t, a, b, "不同 API Key 使用相同 session_id 应产生不同隔离值")
	})

	t.Run("different_raw_different_result", func(t *testing.T) {
		a := IsolateSessionID(1, "session_a")
		b := IsolateSessionID(1, "session_b")
		require.NotEqual(t, a, b)
	})

	t.Run("format_is_16_hex_chars", func(t *testing.T) {
		result := IsolateSessionID(99, "test_session")
		assert.Len(t, result, 16, "应为 16 字符的 hex 字符串")
		for _, ch := range result {
			assert.True(t, (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'),
				"应仅包含 hex 字符: %c", ch)
		}
	})

	t.Run("zero_apiKeyID_still_works", func(t *testing.T) {
		result := IsolateSessionID(0, "session")
		assert.NotEmpty(t, result)
		// Zero remains a distinct API key scope.
		other := IsolateSessionID(1, "session")
		assert.NotEqual(t, result, other)
	})
}

func TestGenerateAnthropicMsgID_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := range 100 {
		id := GenerateAnthropicMsgID()
		require.Regexp(t, `^msg_01[0-9A-Za-z]{22}$`, id)
		_, duplicate := seen[id]
		require.False(t, duplicate, "第 %d 次调用生成了重复 ID: %s", i, id)
		seen[id] = struct{}{}
	}
}
