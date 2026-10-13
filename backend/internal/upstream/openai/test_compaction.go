package openai

import (
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Native compaction probes use a streaming Responses request whose last input
// item is compaction_trigger. Legacy probes use a separate unary request
// without that trigger, and each form retains its own session identifier shape
// for provider capability testing.

// CompactionTestPayload builds a trimmed-model streaming V2 probe ending with
// compaction_trigger. OAuth probes add store=false, while API-key probes leave
// that field absent; callers select the corresponding Responses endpoint and
// beta features.
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
		// OAuth probes use the internal ChatGPT Responses shape, which disables
		// storage; keeping the V2 trigger on that request tests native compaction output.
		payload["store"] = false
	}
	return payload
}

// LegacyCompactionTestPayload builds a trimmed-model unary request for the
// legacy compact endpoint. It contains one user message and no V2 trigger;
// callers select the legacy endpoint and its session format.
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

// CompactionTestHasOutput reports whether SSE or JSON output contains a
// compaction item. It checks raw SSE items, extracted final Responses JSON,
// then the original body; status success by itself is insufficient evidence.
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

// CompactionTestSessionID derives the stable V2 probe session for providerID.
// Nonpositive IDs share an anonymous namespace, while positive IDs select
// separate deterministic sessions under the fixed native-compaction probe
// domain.
func CompactionTestSessionID(providerID int64) string {
	// The literal V2 probe domain keeps the same provider on a stable upstream
	// session across repeated tests and distinguishes it from legacy compact probes.
	if providerID <= 0 {
		return DeriveStableUUIDv4("tokenrouter:openai-native-compaction-v2-probe:anonymous")
	}
	return DeriveStableUUIDv4("tokenrouter:openai-native-compaction-v2-probe:" + strconv.FormatInt(providerID, 10))
}

// LegacyCompactionTestSessionID returns the legacy probe_compact spelling with
// a positive provider suffix. Nonpositive IDs use the unsuffixed session,
// allowing callers to keep legacy probe shape separate from the V2 namespace.
func LegacyCompactionTestSessionID(providerID int64) string {
	if providerID <= 0 {
		return "probe_compact"
	}
	return "probe_compact_" + strconv.FormatInt(providerID, 10)
}
