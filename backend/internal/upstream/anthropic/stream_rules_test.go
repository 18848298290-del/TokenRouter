package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// TestCacheTTLAdaptersPreserveWireShape checks numeric rewrites and retention of unrelated fields.
func TestCacheTTLAdaptersPreserveWireShape(t *testing.T) {
	value := protocol.TokenUsage{CacheCreationInputTokens: 9}
	require.False(t, ApplyCacheTTLOverride(&value, "5m"))
	require.Equal(t, 9, value.CacheCreation5mTokens)
	raw := map[string]any{"cache_creation": map[string]any{"ephemeral_5m_input_tokens": float64(3), "ephemeral_1h_input_tokens": float64(4), "unknown": "kept"}, "cache_creation_input_tokens": float64(99)}
	require.True(t, RewriteCacheCreationJSON(raw, "1h"))
	require.Equal(t, map[string]any{"ephemeral_5m_input_tokens": float64(0), "ephemeral_1h_input_tokens": float64(7), "unknown": "kept"}, raw["cache_creation"])
	require.Equal(t, float64(99), raw["cache_creation_input_tokens"])
	require.False(t, RewriteCacheCreationJSON(raw, "1h"))
	unchanged := map[string]any{"cache_creation": map[string]any{"ephemeral_5m_input_tokens": float64(0), "ephemeral_1h_input_tokens": "7"}}
	require.False(t, RewriteCacheCreationJSON(unchanged, "1h"))
	creation, ok := unchanged["cache_creation"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "7", creation["ephemeral_1h_input_tokens"])
	require.False(t, RewriteCacheCreationJSON(map[string]any{}, "1h"))
}

func TestReconcileCachedTokens_NilUsage(t *testing.T) {
	assert.False(t, ReconcileCachedTokens(nil))
}

func TestReconcileCachedTokens_AlreadyHasCacheRead(t *testing.T) {
	// A positive standard counter takes precedence.
	usage := map[string]any{
		"cache_read_input_tokens": float64(100),
		"cached_tokens":           float64(50),
	}
	assert.False(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(100), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_KimiStyle(t *testing.T) {
	// Kimi supplies a positive cached_tokens value and a zero standard counter.
	usage := map[string]any{
		"input_tokens":                float64(23),
		"cache_creation_input_tokens": float64(0),
		"cache_read_input_tokens":     float64(0),
		"cached_tokens":               float64(23),
	}
	assert.True(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(23), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_NoCachedTokens(t *testing.T) {
	// A native Claude response omits cached_tokens.
	usage := map[string]any{
		"input_tokens":                float64(100),
		"cache_read_input_tokens":     float64(0),
		"cache_creation_input_tokens": float64(0),
	}
	assert.False(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(0), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_CachedTokensZero(t *testing.T) {
	// Zero cached_tokens leaves the response unchanged.
	usage := map[string]any{
		"cache_read_input_tokens": float64(0),
		"cached_tokens":           float64(0),
	}
	assert.False(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(0), usage["cache_read_input_tokens"])
}

func TestReconcileCachedTokens_MissingCacheReadField(t *testing.T) {
	// A positive cached_tokens value also fills a missing standard counter.
	usage := map[string]any{
		"cached_tokens": float64(42),
	}
	assert.True(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(42), usage["cache_read_input_tokens"])
}

func TestStreamingReconcile_MessageStart(t *testing.T) {
	// Kimi message_start carries usage inside message.
	eventJSON := `{
		"type": "message_start",
		"message": {
			"id": "msg_123",
			"type": "message",
			"role": "assistant",
			"model": "kimi",
			"usage": {
				"input_tokens": 23,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens": 0,
				"cached_tokens": 23
			}
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	eventType, _ := event["type"].(string)
	require.Equal(t, "message_start", eventType)

	// Apply the same reconciliation used by the stream processor.
	if msg, ok := event["message"].(map[string]any); ok {
		if u, ok := msg["usage"].(map[string]any); ok {
			ReconcileCachedTokens(u)
		}
	}

	msg, ok := event["message"].(map[string]any)
	require.True(t, ok)
	usage, ok := msg["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(23), usage["cache_read_input_tokens"])

	// The serialized event carries the normalized count.
	data, err := json.Marshal(event)
	require.NoError(t, err)
	assert.Equal(t, int64(23), gjson.GetBytes(data, "message.usage.cache_read_input_tokens").Int())
}

func TestStreamingReconcile_MessageStart_NativeClaude(t *testing.T) {
	// A native Claude event requires no reconciliation.
	eventJSON := `{
		"type": "message_start",
		"message": {
			"usage": {
				"input_tokens": 100,
				"cache_creation_input_tokens": 50,
				"cache_read_input_tokens": 30
			}
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	if msg, ok := event["message"].(map[string]any); ok {
		if u, ok := msg["usage"].(map[string]any); ok {
			ReconcileCachedTokens(u)
		}
	}

	msg, ok := event["message"].(map[string]any)
	require.True(t, ok)
	usage, ok := msg["usage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(30), usage["cache_read_input_tokens"])
}

func TestStreamingReconcile_MessageDelta(t *testing.T) {
	// Kimi message_delta carries usage at the top level.
	eventJSON := `{
		"type": "message_delta",
		"usage": {
			"output_tokens": 7,
			"cache_read_input_tokens": 0,
			"cached_tokens": 15
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	eventType, _ := event["type"].(string)
	require.Equal(t, "message_delta", eventType)

	// Apply the same reconciliation used by the stream processor.
	usage, ok := event["usage"].(map[string]any)
	require.True(t, ok)
	ReconcileCachedTokens(usage)
	assert.Equal(t, float64(15), usage["cache_read_input_tokens"])
}

func TestStreamingReconcile_MessageDelta_NativeClaude(t *testing.T) {
	// The native Claude fixture omits cached_tokens.
	eventJSON := `{
		"type": "message_delta",
		"usage": {
			"output_tokens": 50
		}
	}`

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(eventJSON), &event))

	usage, ok := event["usage"].(map[string]any)
	require.True(t, ok)
	ReconcileCachedTokens(usage)
	_, hasCacheRead := usage["cache_read_input_tokens"]
	assert.False(t, hasCacheRead, "不应为原生 Claude 响应注入 cache_read_input_tokens")
}
