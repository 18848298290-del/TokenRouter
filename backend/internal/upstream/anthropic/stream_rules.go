package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

// stream_rules.go builds Claude Code keepalives and reconciles cache usage
// fields for streamed responses.
// Claude Code keepalives need a delta shape matching the active content block,
// and cache-creation usage needs numeric TTL buckets for billing. The block and
// delta mappings provide the field used by empty keepalive frames, with version
// selection performed against the repository's configured minimum.
//
// Cache reclassification adapts between protocol usage and decoded JSON maps.
// Nested five-minute and one-hour details supply the moved total, while
// aggregate and unknown fields retain their own values. ReconcileCachedTokens
// gives a positive canonical float64 counter precedence over the compatible
// cached_tokens alias. Numeric event indexes accept the representations handled
// here, including float conversion to int; consumers remain responsible for
// usable indexes and serialized mutation of shared event maps.

// ClaudeCodeNoopDeltaKeepaliveMinVersion selects the earliest parsed CLI
// version that uses supported empty content-block deltas in place of idle ping
// frames.
const ClaudeCodeNoopDeltaKeepaliveMinVersion = "2.1.193"

// ShouldUseClaudeCodeNoopDeltaKeepalive reports whether a parsed Claude CLI
// version selects the repository's no-op delta rule. Versions below the
// configured minimum and unrecognized User-Agents return false, selecting
// ordinary pings in StreamResponse.
func ShouldUseClaudeCodeNoopDeltaKeepalive(userAgent string) bool {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return false
	}
	return clientmeta.CompareVersions(version, ClaudeCodeNoopDeltaKeepaliveMinVersion) >= 0
}

// ClaudeCodeKeepaliveDeltaTypeForContentBlock maps text, tool-use, and thinking
// blocks to their empty-delta forms. Unsupported block types return an empty
// string so the stream retains its ordinary ping option.
func ClaudeCodeKeepaliveDeltaTypeForContentBlock(blockType string) string {
	switch blockType {
	case "text":
		return "text_delta"
	case "tool_use":
		return "input_json_delta"
	case "thinking":
		return "thinking_delta"
	default:
		return ""
	}
}

// ClaudeCodeKeepaliveFieldForDeltaType returns the empty-payload field for a
// supported keepalive delta. Unknown delta types return an empty string,
// causing BuildClaudeCodeNoopDeltaKeepalive to report false before constructing
// a frame.
func ClaudeCodeKeepaliveFieldForDeltaType(deltaType string) string {
	switch deltaType {
	case "text_delta":
		return "text"
	case "input_json_delta":
		return "partial_json"
	case "thinking_delta":
		return "thinking"
	default:
		return ""
	}
}

// BuildClaudeCodeNoopDeltaKeepalive returns an empty content-block delta frame
// for a supported deltaType and supplied index. Unsupported types return false;
// callers select an active block index before inserting the frame.
func BuildClaudeCodeNoopDeltaKeepalive(index int, deltaType string) (string, bool) {
	fieldName := ClaudeCodeKeepaliveFieldForDeltaType(deltaType)
	if fieldName == "" {
		return "", false
	}
	return fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"%s\",\"%s\":\"\"}}\n\n", index, deltaType, fieldName), true
}

// SseEventIndex reads supported numeric index representations from a decoded
// event. float64 values truncate toward zero, int64 and json.Number values
// convert to int, and unsupported or noninteger json.Number values return
// false. Callers supply indexes within the usable int range.
func SseEventIndex(event map[string]any) (int, bool) {
	switch v := event["index"].(type) {
	// Decoded JSON indexes discard fractional parts during int conversion.
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		// An unparseable integer cannot select the active keepalive block.
		// Report that index as unavailable so the reader keeps its current
		// supported block choice.
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// ApplyCacheTTLOverride delegates in-place cache-detail classification for
// nonnil usage. Exact target 1h selects one hour; other targets select five
// minutes. The result reports movement between buckets, so filling missing
// five-minute detail from the aggregate can change usage while returning false.
func ApplyCacheTTLOverride(usage *protocol.TokenUsage, target string) bool {
	return protocol.ApplyCacheTTLOverride(usage, target)
}

// RewriteCacheCreationJSON moves the nested cache-detail total into the
// selected TTL bucket and reports that reclassification. It requires a
// cache_creation map, uses the protocol rule's exact 1h selection, and retains
// aggregate and unknown fields.
//
// Supported numeric detail values are read through ParseSSEUsageInt before the
// temporary usage is classified. A change writes both nested buckets as
// float64, matching decoded JSON numbers; a no-op leaves their existing wire
// representations in place. Callers serialize access to the supplied mutable
// map.
func RewriteCacheCreationJSON(usageObj map[string]any, target string) bool {
	ccObj, ok := usageObj["cache_creation"].(map[string]any)
	if !ok {
		return false
	}
	// Cache detail can arrive in several numeric representations. Read both
	// buckets through the protocol parser before selecting their summed TTL
	// classification for this event map.
	v5m, _ := wire.ParseSSEUsageInt(ccObj["ephemeral_5m_input_tokens"])
	v1h, _ := wire.ParseSSEUsageInt(ccObj["ephemeral_1h_input_tokens"])
	projected := protocol.TokenUsage{CacheCreation5mTokens: v5m, CacheCreation1hTokens: v1h}
	if !protocol.ApplyCacheTTLOverride(&projected, target) {
		return false
	}
	// A changed JSON category writes both detail buckets as decoded-number
	// values. Keep the surrounding usage map and aggregate untouched so
	// unrelated response fields remain available.
	ccObj["ephemeral_5m_input_tokens"] = float64(projected.CacheCreation5mTokens)
	ccObj["ephemeral_1h_input_tokens"] = float64(projected.CacheCreation1hTokens)
	return true
}

// ReconcileCachedTokens selects a canonical cache-read count in decoded usage
// and reports an alias insertion. A positive float64 cache_read_input_tokens
// value takes precedence; nil input or an unavailable positive float64
// cached_tokens alias returns false.
//
// A zero, negative, missing, or non-float64 canonical value permits alias
// lookup. A selected alias fills the canonical field while its compatible
// source remains in the map, so later usage extraction and client serialization
// see the same chosen count. Callers supply finite JSON-style counters and
// serialize updates to this mutable map; other numeric representations are
// treated as missing by these float64 assertions.
func ReconcileCachedTokens(usage map[string]any) bool {
	if usage == nil {
		return false
	}
	// Decoded JSON uses float64 counters in this helper. A positive canonical
	// value wins over the compatible alias so one reported cache-read quantity
	// remains selected.
	cacheRead, _ := usage["cache_read_input_tokens"].(float64)
	if cacheRead > 0 {
		return false
	}
	cached, _ := usage["cached_tokens"].(float64)
	if cached <= 0 {
		return false
	}
	usage["cache_read_input_tokens"] = cached
	return true
}
