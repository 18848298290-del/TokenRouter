package openai

import (
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// CompactionTestPayload builds a streaming V2 Responses probe ending with compaction_trigger.
func CompactionTestPayload(model string, isOAuth bool) map[string]any {
	payload := map[string]any{
		"model":        strings.TrimSpace(model),
		"instructions": "You are a helpful coding assistant.",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Respond with OK.",
			},
			map[string]any{"type": "compaction_trigger"},
		},
		"stream": true,
	}
	if isOAuth {
		// The internal ChatGPT Responses API requires store=false for OAuth requests.
		payload["store"] = false
	}
	return payload
}

// LegacyCompactionTestPayload builds a non-streaming probe for the legacy endpoint.
// Use CompactionTestPayload to test V2 support.
func LegacyCompactionTestPayload(model string) map[string]any {
	return map[string]any{
		"model":        strings.TrimSpace(model),
		"instructions": "You are a helpful coding assistant.",
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "Respond with OK.",
			},
		},
	}
}

// CompactionTestHasOutput reports whether the response contains a compaction item.
// A 2xx response alone cannot prove that an intermediary forwarded the trigger.
func CompactionTestHasOutput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	bodyText := string(body)
	if _, found := openai.FindRawCompactionItemFromSSE(bodyText); found {
		return true
	}
	if finalResponse, ok := openai.ExtractCodexFinalResponse(bodyText); ok && openai.ResponsesOutputHasCompactionItem(finalResponse) {
		return true
	}
	return openai.ResponsesOutputHasCompactionItem(body)
}

func CompactionTestSessionID(providerID int64) string {
	// Keep the probe's stable session namespace independent of this helper's name.
	if providerID <= 0 {
		return DeriveStableUUIDv4("tokenrouter:openai-native-compaction-v2-probe:anonymous")
	}
	return DeriveStableUUIDv4("tokenrouter:openai-native-compaction-v2-probe:" + strconv.FormatInt(providerID, 10))
}

// LegacyCompactionTestSessionID uses the legacy probe_compact session format
// so compatibility probes retain the expected request shape.
func LegacyCompactionTestSessionID(providerID int64) string {
	if providerID <= 0 {
		return "probe_compact"
	}
	return "probe_compact_" + strconv.FormatInt(providerID, 10)
}
