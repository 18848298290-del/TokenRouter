package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// Cache compatibility needs canonical counters to win over aliases and TTL
// rewrites to retain unrelated JSON fields. The fixtures exercise zero and
// missing cache counts, nested versus top-level event usage, and the adapter's
// distinction between bucket movement and default detail initialization.

// TestCacheTTLAdaptersPreserveWireShape exercises aggregate default filling,
// numeric nested reclassification, and no-op string retention. It checks false
// can accompany default initialization, while changed buckets retain aggregate
// and unknown JSON fields.
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

// TestReconcileCachedTokens_NilUsage supplies no mutable usage map to the
// reconciliation helper. It checks the missing-input case reports false rather
// than attempting to insert a canonical cache counter.
func TestReconcileCachedTokens_NilUsage(t *testing.T) {
	assert.False(t, ReconcileCachedTokens(nil))
}

// TestReconcileCachedTokens_AlreadyHasCacheRead supplies positive canonical and
// alias values together. It checks the canonical count wins and the helper
// reports no reconciliation for that map.
func TestReconcileCachedTokens_AlreadyHasCacheRead(t *testing.T) {
	// A positive standard counter takes precedence.
	usage := map[string]any{
		"cache_read_input_tokens": float64(100),
		"cached_tokens":           float64(50),
	}
	assert.False(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(100), usage["cache_read_input_tokens"])
}

// TestReconcileCachedTokens_KimiStyle supplies a positive compatible alias
// beside a zero canonical count. It checks reconciliation reports a change and
// copies that alias into the canonical field.
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

// TestReconcileCachedTokens_NoCachedTokens supplies canonical zero with no
// compatible alias. It checks the map's zero remains and the helper reports no
// cache-read reconciliation for that fixture.
func TestReconcileCachedTokens_NoCachedTokens(t *testing.T) {
	// The native-shaped fixture supplies no cached_tokens alias.
	usage := map[string]any{
		"input_tokens":                float64(100),
		"cache_read_input_tokens":     float64(0),
		"cache_creation_input_tokens": float64(0),
	}
	assert.False(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(0), usage["cache_read_input_tokens"])
}

// TestReconcileCachedTokens_CachedTokensZero supplies zero in both cache-read
// representations. It checks no reconciliation is reported and the canonical
// zero remains available in the supplied usage map.
func TestReconcileCachedTokens_CachedTokensZero(t *testing.T) {
	// Zero cached_tokens leaves the response unchanged.
	usage := map[string]any{
		"cache_read_input_tokens": float64(0),
		"cached_tokens":           float64(0),
	}
	assert.False(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(0), usage["cache_read_input_tokens"])
}

// TestReconcileCachedTokens_MissingCacheReadField supplies a positive alias
// without a canonical cache-read key. It checks the helper inserts that count
// and reports the resulting map change.
func TestReconcileCachedTokens_MissingCacheReadField(t *testing.T) {
	// A positive cached_tokens value also fills a missing standard counter.
	usage := map[string]any{
		"cached_tokens": float64(42),
	}
	assert.True(t, ReconcileCachedTokens(usage))
	assert.Equal(t, float64(42), usage["cache_read_input_tokens"])
}

// TestStreamingReconcile_MessageStart decodes a compatible initial event with
// usage nested beneath message. It checks reconciliation fills the canonical
// field in that map and in the serialized event.
func TestStreamingReconcile_MessageStart(t *testing.T) {
	// The compatible initial fixture nests its usage inside message.
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

	// The initial event nests usage inside message. Apply reconciliation at
	// that location before serialization so both the retained map and emitted
	// JSON can expose the selected canonical cache count.
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

	data, err := json.Marshal(event)
	require.NoError(t, err)
	assert.Equal(t, int64(23), gjson.GetBytes(data, "message.usage.cache_read_input_tokens").Int())
}

// TestStreamingReconcile_MessageStart_NativeClaude decodes nested usage with an
// existing positive canonical count. It applies the helper and checks the
// reported native cache-read value remains thirty.
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

	// The canonical count was decoded from the initial event's nested usage.
	// Invoke the helper to check that alias reconciliation leaves it selected
	// in that map.
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

// TestStreamingReconcile_MessageDelta decodes a compatible delta whose usage
// appears at the event's top level. It checks the alias is copied into that
// usage map's canonical cache-read field.
func TestStreamingReconcile_MessageDelta(t *testing.T) {
	// The compatible delta fixture supplies usage at the top level.
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

	usage, ok := event["usage"].(map[string]any)
	require.True(t, ok)
	// This delta carries usage at the event's top level. Reconcile that
	// selected map directly so the compatibility alias feeds the canonical
	// count in the asserted event location.
	ReconcileCachedTokens(usage)
	assert.Equal(t, float64(15), usage["cache_read_input_tokens"])
}

// TestStreamingReconcile_MessageDelta_NativeClaude decodes a delta containing
// output usage and no cache alias. It checks reconciliation leaves the
// cache-read key absent in the native-shaped fixture.
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
