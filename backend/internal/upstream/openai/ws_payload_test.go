package openai

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildReplayTurnPayload builds a full-history replay for turn, adding an item with
// itemBytes of filler per turn to model Codex store=false requests.
func buildReplayTurnPayload(turn, itemBytes int) []byte {
	var b strings.Builder
	_, _ = b.WriteString(`{"type":"response.create","model":"gpt-5.5","stream":true,"input":[`)
	filler := strings.Repeat("x", itemBytes)
	for i := 1; i <= turn; i++ {
		if i > 1 {
			_, _ = b.WriteString(",")
		}
		_, _ = fmt.Fprintf(&b, `{"type":"input_text","text":"turn-%d-%s"}`, i, filler)
	}
	_, _ = b.WriteString(`]}`)
	return []byte(b.String())
}

// TestOpenAIWSReplayStateBuildAllocationBounded checks replay processing across 128 growing turns.
// Payloads are allocated before measurement, so the bound covers parsing and history retention.
// Copying the full history per turn would add quadratic allocation.
func TestOpenAIWSReplayStateBuildAllocationBounded(t *testing.T) {
	const (
		turns     = 128
		itemBytes = 10 * 1024
	)

	payloads := make([][]byte, 0, turns)
	for turn := 1; turn <= turns; turn++ {
		payloads = append(payloads, buildReplayTurnPayload(turn, itemBytes))
	}

	var history []json.RawMessage
	historyExists := false
	delta := []json.RawMessage{json.RawMessage(`{"type":"function_call","id":"item_1","call_id":"call_1","name":"exec","arguments":"{}"}`)}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for turn := 1; turn <= turns; turn++ {
		items, exists, err := BuildOpenAIWSReplayInputSequence(history, historyExists, payloads[turn-1], turn > 1)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, turn)
		// Retain history with the collector delta as the ingress and bridge paths do.
		history = CombineOpenAIWSReplayItems(items, delta)
		historyExists = true
		// The next payload contains all user items but omits the collector item,
		// exercising the common prefix-comparison path on the next iteration.
		history = history[:len(history)-1]
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	// Sharing item bytes leaves room for slice and parser allocations. Copying each turn's
	// full history would allocate over 80 MiB (the sum of turn * 10 KiB),
	// which exceeds this limit.
	const maxAllocatedBytes = 32 * 1024 * 1024
	require.Lessf(
		t,
		allocated,
		uint64(maxAllocatedBytes),
		"replay 状态构建累计分配 %d bytes 超出上界，疑似回归为逐轮深拷贝",
		allocated,
	)
}

func TestApplyOpenAIWSRetryPayloadStrategy_KeepPromptCacheKey(t *testing.T) {
	payload := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "pcache_123",
		"include":          []any{"reasoning.encrypted_content"},
		"text": map[string]any{
			"verbosity": "low",
		},
		"tools": []any{map[string]any{"type": "function"}},
	}

	strategy, removed := ApplyWSRetryPayloadStrategy(payload, 3)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Contains(t, removed, "include")
	require.NotContains(t, removed, "prompt_cache_key")
	require.Equal(t, "pcache_123", payload["prompt_cache_key"])
	require.NotContains(t, payload, "include")
	require.Contains(t, payload, "text")
}

func TestApplyOpenAIWSRetryPayloadStrategy_AttemptSixKeepsSemanticFields(t *testing.T) {
	payload := map[string]any{
		"prompt_cache_key":    "pcache_456",
		"instructions":        "long instructions",
		"tools":               []any{map[string]any{"type": "function"}},
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"include":             []any{"reasoning.encrypted_content"},
		"text":                map[string]any{"verbosity": "high"},
	}

	strategy, removed := ApplyWSRetryPayloadStrategy(payload, 6)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Contains(t, removed, "include")
	require.NotContains(t, removed, "prompt_cache_key")
	require.Equal(t, "pcache_456", payload["prompt_cache_key"])
	require.Contains(t, payload, "instructions")
	require.Contains(t, payload, "tools")
	require.Contains(t, payload, "tool_choice")
	require.Contains(t, payload, "parallel_tool_calls")
	require.Contains(t, payload, "text")
}
