package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	wire "github.com/TokenFlux/TokenRouter/internal/protocol/anthropic"
)

const ClaudeCodeNoopDeltaKeepaliveMinVersion = "2.1.193"

func ShouldUseClaudeCodeNoopDeltaKeepalive(userAgent string) bool {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return false
	}
	return clientmeta.CompareVersions(version, ClaudeCodeNoopDeltaKeepaliveMinVersion) >= 0
}

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

func BuildClaudeCodeNoopDeltaKeepalive(index int, deltaType string) (string, bool) {
	fieldName := ClaudeCodeKeepaliveFieldForDeltaType(deltaType)
	if fieldName == "" {
		return "", false
	}
	return fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"%s\",\"%s\":\"\"}}\n\n", index, deltaType, fieldName), true
}

func SseEventIndex(event map[string]any) (int, bool) {
	switch v := event["index"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// ApplyCacheTTLOverride moves cache creation tokens to target ("5m" or "1h").
// It reports whether the usage changed.
func ApplyCacheTTLOverride(usage *protocol.TokenUsage, target string) bool {
	return protocol.ApplyCacheTTLOverride(usage, target)
}

// RewriteCacheCreationJSON reclassifies nested cache_creation counters in usageObj
// and reports whether it changed them.
func RewriteCacheCreationJSON(usageObj map[string]any, target string) bool {
	ccObj, ok := usageObj["cache_creation"].(map[string]any)
	if !ok {
		return false
	}
	v5m, _ := wire.ParseSSEUsageInt(ccObj["ephemeral_5m_input_tokens"])
	v1h, _ := wire.ParseSSEUsageInt(ccObj["ephemeral_1h_input_tokens"])
	projected := protocol.TokenUsage{CacheCreation5mTokens: v5m, CacheCreation1hTokens: v1h}
	if !protocol.ApplyCacheTTLOverride(&projected, target) {
		return false
	}
	ccObj["ephemeral_5m_input_tokens"] = float64(projected.CacheCreation5mTokens)
	ccObj["ephemeral_1h_input_tokens"] = float64(projected.CacheCreation1hTokens)
	return true
}

// ReconcileCachedTokens fills the Anthropic cache-read counter from a positive
// OpenAI-style cached_tokens value when the standard counter is not positive.
func ReconcileCachedTokens(usage map[string]any) bool {
	if usage == nil {
		return false
	}
	cacheRead, _ := usage["cache_read_input_tokens"].(float64)
	if cacheRead > 0 {
		return false // Retain a positive standard counter.
	}
	cached, _ := usage["cached_tokens"].(float64)
	if cached <= 0 {
		return false
	}
	usage["cache_read_input_tokens"] = cached
	return true
}
