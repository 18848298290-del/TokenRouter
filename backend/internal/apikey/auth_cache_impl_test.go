package apikey

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCurrentSnapshotDropsRetiredFields rejects old versions and compares explicitly migrated fixtures with current serialization.
func TestCurrentSnapshotDropsRetiredFields(t *testing.T) {
	for _, kind := range []string{"full", "empty"} {
		t.Run(kind, func(t *testing.T) {
			original, e := os.ReadFile("testdata/v40-" + kind + ".json")
			require.NoError(t, e)
			var entry APIKeyAuthCacheEntry
			entry.Snapshot = &APIKeyAuthSnapshot{Version: 40}
			cached, used, err := new(APIKeyService).KeyApplyAuthCacheEntry("legacy-v40", &entry)
			require.NoError(t, err)
			require.False(t, used)
			require.Nil(t, cached)
			var current map[string]any
			require.NoError(t, json.Unmarshal(original, &current))
			migrateSnapshotPricingFields(current)
			migrated, err := json.Marshal(current)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(migrated, &entry))
			actual, e := json.Marshal(entry)
			require.NoError(t, e)
			var expected, decoded map[string]any
			require.NoError(t, json.Unmarshal(migrated, &expected))
			require.NoError(t, json.Unmarshal(actual, &decoded))
			var stripPolicy func(any)
			stripPolicy = func(value any) {
				switch v := value.(type) {
				case map[string]any:
					delete(v, "routing_policy")
					for _, child := range v {
						stripPolicy(child)
					}
				case []any:
					for _, child := range v {
						stripPolicy(child)
					}
				}
			}
			stripPolicy(decoded)
			require.Equal(t, expected, decoded)
		})
	}
}

// migrateSnapshotPricingFields updates test expectations; production reloads obsolete snapshots.
func migrateSnapshotPricingFields(value any) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, "platform")
		delete(node, "peak_rate_enabled")
		delete(node, "peak_start")
		delete(node, "peak_end")
		delete(node, "peak_rate_multiplier")
		delete(node, "long_context_pricing_enabled")
		delete(node, "free_openai_fast")
		delete(node, "batch_image_discount_multiplier")
		delete(node, "batch_image_hold_multiplier")
		delete(node, "web_search_price_per_call")
		delete(node, "search_price_per_1k")
		delete(node, "audio_realtime_price_per_min")
		delete(node, "audio_tts_price_per_million_chars")
		delete(node, "audio_stt_price_per_hour")
		delete(node, "model_pricing")

		delete(node, "messages_dispatch_model_config")
		delete(node, "opus_mapped_model")
		delete(node, "sonnet_mapped_model")
		delete(node, "haiku_mapped_model")
		if old, ok := node["fallback_to_default_group_when_unavailable"]; ok {
			node["fallback_when_group_unavailable"] = old
			delete(node, "fallback_to_default_group_when_unavailable")
		}
		if fallback, ok := node["protocol_fallbacks"].(map[string]any); ok {
			for source, target := range fallback {
				if text, ok := target.(string); ok {
					fallback[source] = []any{text}
				}
			}
		}
		if id, ok := node["channel_id"]; ok {
			node["pricing_config_id"] = id
			delete(node, "channel_id")
		}
		if _, ok := node["version"]; ok {
			node["version"] = KeyApiKeyAuthSnapshotVersion
			node["concurrency_limit"] = float64(0)
			node["rpm_limit"] = float64(0)
		}
		for _, child := range node {
			migrateSnapshotPricingFields(child)
		}
	case []any:
		for _, child := range node {
			migrateSnapshotPricingFields(child)
		}
	}
}
