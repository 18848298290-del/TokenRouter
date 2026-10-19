package provider

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// TestApplyCodexOAuthTransform_ToolContinuationPreservesInput transforms an
// item reference and an isolated function output under store=false. The reference
// ID and output item ID remain available, while call_id receives the
// function-call prefix.
func TestApplyCodexOAuthTransform_ToolContinuationPreservesInput(t *testing.T) {
	// Continuation requests retain item references and IDs with store=false.

	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "item_reference", "id": "ref1", "text": "x"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok", "id": "o1"},
		},
		"tool_choice": "auto",
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	// Storage defaults to false.
	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "item_reference", first["type"])
	require.Equal(t, "ref1", first["id"])

	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "o1", second["id"])
	require.Equal(t, "fc_1", second["call_id"])
}

// TestApplyCodexOAuthTransform_MessagesBridgePromptCacheKeyIsHeaderOnly
// supplies a Messages bridge marker and an Anthropic metadata cache key.
// Transformation returns that key for header use, marks the body changed, and
// removes prompt_cache_key from the body.
func TestApplyCodexOAuthTransform_MessagesBridgePromptCacheKeyIsHeaderOnly(t *testing.T) {
	reqBody := map[string]any{
		"model":            "gpt-5.5",
		"prompt_cache_key": "anthropic-metadata-session-1",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "developer",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": OpenAICompatClaudeCodeTodoGuardMarker,
					},
				},
			},
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": "hello",
			},
		},
	}

	result := ApplyCodexOAuthTransformWithOptions(reqBody, openai.CodexOAuthTransformOptions{
		SkipDefaultInstructions: true,
		PreserveToolCallIDs:     true,
	})

	require.Equal(t, "anthropic-metadata-session-1", result.PromptCacheKey)
	require.True(t, result.Modified)
	require.NotContains(t, reqBody, "prompt_cache_key")
}

// TestApplyCodexOAuthTransform_ToolContinuationPreservesNativeMessageAndReasoningIDs
// supplies a message and a reasoning item reference under continuation. Their
// native message and reference identifiers remain intact after OAuth body
// transformation.
func TestApplyCodexOAuthTransform_ToolContinuationPreservesNativeMessageAndReasoningIDs(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "message", "id": "msg_0", "role": "user", "content": "hi"},
			map[string]any{"type": "item_reference", "id": "rs_123"},
		},
		"tool_choice": "auto",
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "msg_0", first["id"])

	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "rs_123", second["id"])
}

// TestApplyCodexOAuthTransform_ToolContinuationNormalizesToolReferenceIDsOnly
// pairs a legacy call_ reference with a function output. Both identifiers
// become fc_1, retaining their relationship across the transformed input
// items.
func TestApplyCodexOAuthTransform_ToolContinuationNormalizesToolReferenceIDsOnly(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "item_reference", "id": "call_1"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
		},
		"tool_choice": "auto",
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_1", first["id"])

	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_1", second["call_id"])
}

// TestApplyCodexOAuthTransform_NormalizesIsolatedLegacyReferenceAcrossTurns
// supplies legacy, native, and vendor item references without matching local
// calls. Legacy call_ becomes fc_, while native and opaque vendor references
// retain their existing values.
func TestApplyCodexOAuthTransform_NormalizesIsolatedLegacyReferenceAcrossTurns(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "item_reference", "id": "call_previous_turn"},
			map[string]any{"type": "item_reference", "id": "fc_remote_item"},
			map[string]any{"type": "item_reference", "id": "vendor_remote_item"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 3)
	for i, expectedID := range []string{"fc_previous_turn", "fc_remote_item", "vendor_remote_item"} {
		item, itemOK := input[i].(map[string]any)
		require.True(t, itemOK)
		require.Equal(t, expectedID, item["id"])
	}
}

// TestApplyCodexOAuthTransform_BoundsLongCallIDsAndPreservesPairing supplies
// boundary, oversized, and equivalent-prefix call/output pairs. Resulting IDs
// share deterministic function-call replacements and stay within
// CodexCallIDMaxLength after normalization.
func TestApplyCodexOAuthTransform_BoundsLongCallIDsAndPreservesPairing(t *testing.T) {
	suffix := strings.Repeat("z", 62)
	for _, tc := range []struct {
		name         string // call identity scenario label
		callID       string // incoming function call identity
		outputCallID string // paired output call identity
	}{
		{name: "non-native boundary id", callID: "call-" + strings.Repeat("x", 59), outputCallID: "call-" + strings.Repeat("x", 59)},
		{name: "overlong fc id", callID: "fc_" + strings.Repeat("y", 62), outputCallID: "fc_" + strings.Repeat("y", 62)},
		{name: "equivalent prefixes", callID: "call_" + suffix, outputCallID: "fc_" + suffix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := map[string]any{
				"model": "gpt-5.2",
				"input": []any{
					map[string]any{"type": "function_call", "call_id": tc.callID, "name": "shell"},
					map[string]any{"type": "function_call_output", "call_id": tc.outputCallID, "output": "done"},
				},
			}

			ApplyCodexOAuthTransform(reqBody, false, false)

			input, ok := reqBody["input"].([]any)
			require.True(t, ok)
			call, ok := input[0].(map[string]any)
			require.True(t, ok)
			output, ok := input[1].(map[string]any)
			require.True(t, ok)

			fixedCallID, ok := call["call_id"].(string)
			require.True(t, ok)
			require.LessOrEqual(t, len(fixedCallID), openai.CodexCallIDMaxLength)
			require.True(t, strings.HasPrefix(fixedCallID, openai.CodexCallIDPrefix))
			require.Equal(t, fixedCallID, output["call_id"])
			require.Equal(t, fixedCallID, openai.NormalizeCodexCallID(tc.callID))
			require.Equal(t, fixedCallID, openai.NormalizeCodexCallID(tc.outputCallID))
		})
	}
}

// TestApplyCodexOAuthTransform_PreservesCallIDsWithinLimitWhenRequested
// enables preserve mode for Anthropic and exact-limit call IDs. Both function
// calls and their outputs retain the supplied IDs when their lengths fit the
// allowed limit.
func TestApplyCodexOAuthTransform_PreservesCallIDsWithinLimitWhenRequested(t *testing.T) {
	// Preserve mode passes IDs of up to 64 characters through unchanged,
	// including IDs exactly at the limit.
	for _, tc := range []struct {
		name   string // preserved call identity case
		callID string // requested preserved call identity
	}{
		{name: "anthropic toolu id", callID: "toolu_01ABCdefGHIjklMNOpqrsTUV"},
		{name: "boundary 64 chars", callID: "toolu_" + strings.Repeat("x", openai.CodexCallIDMaxLength-len("toolu_"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.LessOrEqual(t, len(tc.callID), openai.CodexCallIDMaxLength)
			reqBody := map[string]any{
				"model": "gpt-5.2",
				"input": []any{
					map[string]any{"type": "function_call", "call_id": tc.callID, "name": "shell"},
					map[string]any{"type": "function_call_output", "call_id": tc.callID, "output": "done"},
				},
			}

			ApplyCodexOAuthTransformWithOptions(reqBody, openai.CodexOAuthTransformOptions{
				PreserveToolCallIDs: true,
			})

			input, ok := reqBody["input"].([]any)
			require.True(t, ok)
			call, ok := input[0].(map[string]any)
			require.True(t, ok)
			output, ok := input[1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, tc.callID, call["call_id"])
			require.Equal(t, tc.callID, output["call_id"])
		})
	}
}

// TestApplyCodexOAuthTransform_CompactsOverlongCallIDsWhenPreserveRequested
// enables preserve mode for a 78-character call ID. Calls and outputs still
// receive the same deterministic compacted identifier of CodexCallIDMaxLength.
func TestApplyCodexOAuthTransform_CompactsOverlongCallIDsWhenPreserveRequested(t *testing.T) {
	// IDs longer than 64 characters must be shortened even in preserve mode.
	// Calls and their outputs must receive the same deterministic replacement.
	callID := "srvtoolu_" + strings.Repeat("x", 69)
	require.Len(t, callID, 78)
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "function_call", "call_id": callID, "name": "shell"},
			map[string]any{"type": "function_call_output", "call_id": callID, "output": "done"},
		},
	}

	ApplyCodexOAuthTransformWithOptions(reqBody, openai.CodexOAuthTransformOptions{
		PreserveToolCallIDs: true,
	})

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	call, ok := input[0].(map[string]any)
	require.True(t, ok)
	output, ok := input[1].(map[string]any)
	require.True(t, ok)

	compacted, ok := call["call_id"].(string)
	require.True(t, ok)
	require.Len(t, compacted, openai.CodexCallIDMaxLength)
	require.True(t, strings.HasPrefix(compacted, openai.CodexCallIDPrefix))
	require.Equal(t, compacted, output["call_id"], "两侧压缩结果必须一致以保持配对")
	require.Equal(t, openai.NormalizeCodexCallID(callID), compacted, "压缩必须是确定性的")
}

// TestApplyCodexOAuthTransform_ToolSearchOutputPreservesCallID transforms an
// isolated tool-search output with a legacy call prefix. The output type
// remains tool_search_output and its call_id uses the tool-search prefix.
func TestApplyCodexOAuthTransform_ToolSearchOutputPreservesCallID(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "tool_search_output", "call_id": "call_1", "output": "ok"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tool_search_output", first["type"])
	require.Equal(t, "tsc_1", first["call_id"])
}

// TestApplyCodexOAuthTransform_CustomAndMCPToolOutputsPreserveCallID
// transforms isolated custom and MCP outputs carrying legacy call IDs. Their
// resulting IDs use custom-tool and function-call prefixes respectively, with
// both items retained.
func TestApplyCodexOAuthTransform_CustomAndMCPToolOutputsPreserveCallID(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_custom", "output": "ok"},
			map[string]any{"type": "mcp_tool_call_output", "call_id": "call_mcp", "output": "ok"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ctc_custom", first["call_id"])

	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_mcp", second["call_id"])
}

// TestApplyCodexOAuthTransform_NormalizesNativeToolCallPairsByType supplies
// custom and search calls with invalid replay IDs plus outputs referencing
// those IDs. Invalid item IDs disappear, and each call/output pair receives
// its corresponding type-specific call_id.
func TestApplyCodexOAuthTransform_NormalizesNativeToolCallPairsByType(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			map[string]any{"type": "custom_tool_call", "id": "fc_custom", "call_id": "call_custom", "name": "apply_patch"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "fc_custom", "output": "done"},
			map[string]any{"type": "tool_search_call", "id": "fc_search", "call_id": "call_search"},
			map[string]any{"type": "tool_search_output", "call_id": "fc_search", "output": "result"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 4)
	custom, ok := input[0].(map[string]any)
	require.True(t, ok)
	customOutput, ok := input[1].(map[string]any)
	require.True(t, ok)
	search, ok := input[2].(map[string]any)
	require.True(t, ok)
	searchOutput, ok := input[3].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, custom, "id", "the invalid replay item id must be removed, not fabricated")
	require.Equal(t, "ctc_custom", custom["call_id"])
	require.Equal(t, custom["call_id"], customOutput["call_id"])
	require.NotContains(t, search, "id", "the invalid replay item id must be removed, not fabricated")
	require.Equal(t, "tsc_search", search["call_id"])
	require.Equal(t, search["call_id"], searchOutput["call_id"])
}

// TestApplyCodexOAuthTransform_PreservesNativeCallIDsWhenRequested enables
// preserve mode for custom and search calls with valid item IDs. Both item
// identifiers and each supplied paired call_id survive transformation.
func TestApplyCodexOAuthTransform_PreservesNativeCallIDsWhenRequested(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			map[string]any{"type": "custom_tool_call", "id": "ctc_custom", "call_id": "call_custom", "name": "apply_patch"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "call_custom", "output": "done"},
			map[string]any{"type": "tool_search_call", "id": "tsc_search", "call_id": "call_search"},
			map[string]any{"type": "tool_search_output", "call_id": "call_search", "output": "result"},
		},
	}

	ApplyCodexOAuthTransformWithOptions(reqBody, openai.CodexOAuthTransformOptions{PreserveToolCallIDs: true})

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 4)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	third, ok := input[2].(map[string]any)
	require.True(t, ok)
	fourth, ok := input[3].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ctc_custom", first["id"])
	require.Equal(t, "call_custom", first["call_id"])
	require.Equal(t, "call_custom", second["call_id"])
	require.Equal(t, "tsc_search", third["id"])
	require.Equal(t, "call_search", third["call_id"])
	require.Equal(t, "call_search", fourth["call_id"])
}

// TestApplyCodexOAuthTransform_BoundsEquivalentNativeToolCallIDsWithPairing
// uses oversized equivalent call_ and fc_ values for a custom call/output
// pair. Preserve mode still produces matching bounded ctc_ identifiers for
// both items.
func TestApplyCodexOAuthTransform_BoundsEquivalentNativeToolCallIDsWithPairing(t *testing.T) {
	suffix := strings.Repeat("x", 70)
	reqBody := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			map[string]any{"type": "custom_tool_call", "call_id": "call_" + suffix, "name": "apply_patch"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "fc_" + suffix, "output": "done"},
		},
	}

	ApplyCodexOAuthTransformWithOptions(reqBody, openai.CodexOAuthTransformOptions{PreserveToolCallIDs: true})

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	callID, ok := first["call_id"].(string)
	require.True(t, ok)
	outputCallID, ok := second["call_id"].(string)
	require.True(t, ok)
	require.LessOrEqual(t, len(callID), openai.CodexCallIDMaxLength)
	require.True(t, strings.HasPrefix(callID, "ctc_"))
	require.Equal(t, callID, outputCallID)
}

// TestApplyCodexOAuthTransform_ImageAndWebSearchCallsDoNotGainCallID supplies
// an image call with a valid item ID and a web-search call carrying call_id.
// Image identity survives while both non-pair item types end without call_id.
func TestApplyCodexOAuthTransform_ImageAndWebSearchCallsDoNotGainCallID(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "image_generation_call", "id": "ig_123", "status": "completed"},
			map[string]any{"type": "web_search_call", "call_id": "call_bad", "status": "completed"},
		},
		"tool_choice": "auto",
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ig_123", first["id"])
	_, hasCallID := first["call_id"]
	require.False(t, hasCallID)

	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	_, hasCallID = second["call_id"]
	require.False(t, hasCallID)
}

// TestApplyCodexOAuthTransform_ConvertsToolRoleMessageToFunctionCallOutput
// supplies a Chat-style tool-role message with content and tool_call_id.
// Transformation produces a function output, normalizes its paired ID, retains
// output text, and removes role.
func TestApplyCodexOAuthTransform_ConvertsToolRoleMessageToFunctionCallOutput(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": []any{
			map[string]any{
				"type":         "message",
				"role":         "tool",
				"tool_call_id": "call_1",
				"content":      "ok",
			},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call_output", item["type"])
	require.Equal(t, "fc_1", item["call_id"])
	require.Equal(t, "ok", item["output"])
	_, hasRole := item["role"]
	require.False(t, hasRole)
}

// TestApplyCodexOAuthTransform_StringifiesNonStringMessageContentText places
// an array in an input_text part's text field. OAuth transformation retains
// the content part and encodes that value as the JSON string for the array.
func TestApplyCodexOAuthTransform_StringifiesNonStringMessageContentText(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": []any{"a", "b"}},
				},
			},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	content, ok := item["content"].([]any)
	require.True(t, ok)
	part, ok := content[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, `["a","b"]`, part["text"])
}

// TestApplyCodexOAuthTransform_DowngradesUnknownToolChoice selects a custom
// tool type while only a function declaration exists. Transformation replaces
// that unmatched selection with auto for the resulting request.
func TestApplyCodexOAuthTransform_DowngradesUnknownToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
		},
		"tool_choice": map[string]any{"type": "custom"},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	require.Equal(t, "auto", reqBody["tool_choice"])
}

// TestApplyCodexOAuthTransform_PreservesKnownToolChoice selects custom with a
// matching custom tool declaration. The transformed tool-choice object remains
// present and retains its custom type.
func TestApplyCodexOAuthTransform_PreservesKnownToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"tools": []any{
			map[string]any{"type": "custom", "name": "shell"},
		},
		"tool_choice": map[string]any{"type": "custom"},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	choice, ok := reqBody["tool_choice"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "custom", choice["type"])
}

// TestApplyCodexOAuthTransform_NormalizesLegacyFunctionToolChoice uses the
// nested Chat function-choice object with a declared function. Transformation
// produces flat Responses type and name fields and removes the nested function
// object.
func TestApplyCodexOAuthTransform_NormalizesLegacyFunctionToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
		},
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "shell"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	choice, ok := reqBody["tool_choice"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function", choice["type"])
	require.Equal(t, "shell", choice["name"])
	require.NotContains(t, choice, "function")
}

// TestApplyCodexOAuthTransform_DowngradesMissingFunctionToolChoice selects a
// nested function name missing from the declared tools. The transformed
// request uses auto, allowing the remaining declared function list to govern
// selection.
func TestApplyCodexOAuthTransform_DowngradesMissingFunctionToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
		},
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "missing"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	require.Equal(t, "auto", reqBody["tool_choice"])
}

// TestApplyCodexOAuthTransform_AddsFallbackNameForFunctionCallInput supplies a
// function call lacking a name beside a user message. The transformed call
// retains its type, receives the fallback name tool, and normalizes call_id.
func TestApplyCodexOAuthTransform_AddsFallbackNameForFunctionCallInput(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "run tool"},
			map[string]any{"type": "function_call", "call_id": "call_1", "arguments": "{}"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	item, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call", item["type"])
	require.Equal(t, "tool", item["name"])
	require.Equal(t, "fc_1", item["call_id"])
}

// TestApplyCodexOAuthTransform_PreservesFunctionCallInputName transforms a
// named custom-tool call with a legacy identifier. The supplied shell name
// survives while call_id receives the custom-tool prefix.
func TestApplyCodexOAuthTransform_PreservesFunctionCallInputName(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": []any{
			map[string]any{"type": "custom_tool_call", "call_id": "call_1", "name": "shell", "input": "pwd"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "shell", item["name"])
	require.Equal(t, "ctc_1", item["call_id"])
}

// TestApplyCodexOAuthTransform_PreservesMCPToolCallIDAndName supplies a named
// MCP call with arguments and a legacy call identifier. The type and remote
// tool name remain, while the identifier normalizes to fc_abc.
func TestApplyCodexOAuthTransform_PreservesMCPToolCallIDAndName(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": []any{
			map[string]any{
				"type":      "mcp_tool_call",
				"call_id":   "call_abc",
				"name":      "remote_tool",
				"arguments": "{}",
			},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "mcp_tool_call", item["type"])
	require.Equal(t, "remote_tool", item["name"])
	require.Equal(t, "fc_abc", item["call_id"])
}

// TestCodexInputItemRequiresNameTypesAllowCallID checks function, custom-tool,
// and MCP call types against the upstream predicates. Each listed type both
// requires a name and belongs to the call-ID-capable tool set.
func TestCodexInputItemRequiresNameTypesAllowCallID(t *testing.T) {
	for _, typ := range []string{"function_call", "custom_tool_call", "mcp_tool_call"} {
		require.True(t, openai.CodexInputItemRequiresName(typ), typ)
		require.True(t, openai.IsCodexToolCallItemType(typ), typ)
	}
}

// TestApplyCodexOAuthTransform_ExplicitStoreFalsePreserved supplies
// store=false alongside a continuation output. The transformed request retains
// a boolean storage field with false as its value.
func TestApplyCodexOAuthTransform_ExplicitStoreFalsePreserved(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.1",
		"store": false,
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
		},
		"tool_choice": "auto",
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)
}

// TestApplyCodexOAuthTransform_ExplicitStoreTrueForcedFalse supplies
// store=true alongside a continuation output. OAuth transformation replaces
// that value with boolean false for the internal Responses request.
func TestApplyCodexOAuthTransform_ExplicitStoreTrueForcedFalse(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.1",
		"store": true,
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
		},
		"tool_choice": "auto",
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)
}

// TestApplyCodexOAuthTransform_CompactForcesNonStreaming supplies storage and
// streaming flags on a Compact request. Both fields are removed and the
// transformation reports modification for the resulting Compact body.
func TestApplyCodexOAuthTransform_CompactForcesNonStreaming(t *testing.T) {
	reqBody := map[string]any{
		"model":  "gpt-5.1-codex",
		"store":  true,
		"stream": true,
	}

	result := ApplyCodexOAuthTransform(reqBody, true, true)

	_, hasStore := reqBody["store"]
	require.False(t, hasStore)
	_, hasStream := reqBody["stream"]
	require.False(t, hasStream)
	require.True(t, result.Modified)
}

// TestApplyCodexOAuthTransform_NonContinuationDefaultsStoreFalseAndStripsIDs
// supplies a plain text item with an ID and no continuation. Transformation
// adds boolean store=false, keeps the item, and removes its input ID.
func TestApplyCodexOAuthTransform_NonContinuationDefaultsStoreFalseAndStripsIDs(t *testing.T) {
	// Requests without continuation default to store=false and strip input IDs.

	reqBody := map[string]any{
		"model": "gpt-5.1",
		"input": []any{
			map[string]any{"type": "text", "id": "t1", "text": "hi"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasID := item["id"]
	require.False(t, hasID)
}

// TestApplyCodexOAuthTransform_PreservesReasoningInput includes a reasoning
// item with an empty summary beside a user message. Transformation keeps
// reasoning and its summary but removes the replay item ID.
func TestApplyCodexOAuthTransform_PreservesReasoningInput(t *testing.T) {
	// The HTTP conversion path retains reasoning items for interleaved thinking across turns.
	reqBody := map[string]any{
		"model": "gpt-5.1",
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{
				"type":    "reasoning",
				"id":      "rs_0672f12450da0b9c0169f07220a6c08198b68c2455ced99344",
				"summary": []any{},
			},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	reasoning, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "reasoning", reasoning["type"])
	require.Equal(t, []any{}, reasoning["summary"])
	require.NotContains(t, reasoning, "id")
}

// TestFilterCodexInput_RemovesItemReferenceWhenNotPreserved disables reference
// preservation for an item reference followed by text. Filtering drops the
// reference, retains the text item, and removes its ID.
func TestFilterCodexInput_RemovesItemReferenceWhenNotPreserved(t *testing.T) {
	input := []any{
		map[string]any{"type": "item_reference", "id": "ref1"},
		map[string]any{"type": "text", "id": "t1", "text": "hi"},
	}

	filtered := openai.FilterCodexInputWithOptions(input, openai.CodexInputFilterOptions{PreserveReferences: false})
	require.Len(t, filtered, 1)

	item, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", item["type"])
	_, hasID := item["id"]
	require.False(t, hasID)
}

// TestApplyCodexOAuthTransform_NormalizeCodexTools_PreservesResponsesFunctionTools
// mixes a valid flat Responses function with a null nested function. The
// transformed tools retain the valid function and discard the malformed
// declaration.
func TestApplyCodexOAuthTransform_NormalizeCodexTools_PreservesResponsesFunctionTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.1",
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "bash",
				"description": "desc",
				"parameters":  map[string]any{"type": "object"},
			},
			map[string]any{
				"type":     "function",
				"function": nil,
			},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)

	first, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function", first["type"])
	require.Equal(t, "bash", first["name"])
}

// TestNormalizeOpenAIResponsesImageGenerationTools_RewritesLegacyFields
// supplies format and compression on an image-generation tool. Normalization
// moves their values to output_format and output_compression and removes both
// legacy fields.
func TestNormalizeOpenAIResponsesImageGenerationTools_RewritesLegacyFields(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{
			map[string]any{
				"type":        "image_generation",
				"format":      "png",
				"compression": 60,
			},
		},
	}

	modified := openai.NormalizeOpenAIResponsesImageGenerationTools(reqBody)
	require.True(t, modified)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	first, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "png", first["output_format"])
	require.Equal(t, 60, first["output_compression"])
	_, hasFormat := first["format"]
	require.False(t, hasFormat)
	_, hasCompression := first["compression"]
	require.False(t, hasCompression)
}

// TestEnsureOpenAIResponsesImageGenerationTool_NoTools supplies a non-Spark
// request with no tool list. Injection reports a change and creates one
// image_generation tool whose default output format is png.
func TestEnsureOpenAIResponsesImageGenerationTool_NoTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": "draw a cat",
	}

	modified := EnsureOpenAIResponsesImageGenerationTool(reqBody)
	require.True(t, modified)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image_generation", tool["type"])
	require.Equal(t, "png", tool["output_format"])
}

// TestEnsureOpenAIResponsesImageGenerationTool_SkipsSpark attempts image-tool
// injection on an exact Spark model request. It reports no modification and
// leaves the tools field absent.
func TestEnsureOpenAIResponsesImageGenerationTool_SkipsSpark(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"input": "draw a cat",
	}

	modified := EnsureOpenAIResponsesImageGenerationTool(reqBody)
	require.False(t, modified)
	require.NotContains(t, reqBody, "tools")
}

// TestEnsureOpenAIResponsesImageGenerationTool_AppendsToExistingTools starts
// with a web-search tool on a non-Spark request. Injection keeps that
// declaration and appends one hosted image tool with png output.
func TestEnsureOpenAIResponsesImageGenerationTool_AppendsToExistingTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"tools": []any{
			map[string]any{"type": "web_search"},
		},
	}

	modified := EnsureOpenAIResponsesImageGenerationTool(reqBody)
	require.True(t, modified)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 2)
	first, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "web_search", first["type"])
	second, ok := tools[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image_generation", second["type"])
	require.Equal(t, "png", second["output_format"])
}

// TestEnsureOpenAIResponsesImageGenerationTool_PreservesExistingImageTool
// supplies an image tool requesting webp beside web search. Injection reports
// no change, retains both tools, and keeps the image format selected by the
// client.
func TestEnsureOpenAIResponsesImageGenerationTool_PreservesExistingImageTool(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"tools": []any{
			map[string]any{"type": "image_generation", "output_format": "webp"},
			map[string]any{"type": "web_search"},
		},
	}

	modified := EnsureOpenAIResponsesImageGenerationTool(reqBody)
	require.False(t, modified)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 2)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "webp", tool["output_format"])
}

// TestEnsureOpenAIResponsesImageGenerationTool_PreservesImageGenNamespace
// declares image_gen in top-level tools or Responses Lite additional_tools.
// Recognition finds both forms and injection leaves them free of a duplicate
// hosted image_generation declaration.
func TestEnsureOpenAIResponsesImageGenerationTool_PreservesImageGenNamespace(t *testing.T) {
	tests := []struct {
		name    string         // image declaration scenario label
		reqBody map[string]any // mutable image declaration request
	}{
		{
			name: "top-level tools",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"tools": []any{
					map[string]any{
						"type": "namespace",
						"name": "image_gen",
						"tools": []any{
							map[string]any{"type": "function", "name": "imagegen"},
						},
					},
				},
			},
		},
		{
			name: "responses lite additional_tools",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"input": []any{
					map[string]any{
						"type": "additional_tools",
						"tools": []any{
							map[string]any{
								"type": "namespace",
								"name": "image_gen",
								"tools": []any{
									map[string]any{"type": "function", "name": "imagegen"},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, openai.HasOpenAIImageGenerationTool(tt.reqBody))

			modified := EnsureOpenAIResponsesImageGenerationTool(tt.reqBody)

			require.False(t, modified)
			tools, _ := tt.reqBody["tools"].([]any)
			for _, rawTool := range tools {
				tool, ok := rawTool.(map[string]any)
				require.True(t, ok)
				require.NotEqual(t, "image_generation", openai.FirstNonEmptyString(tool["type"]))
			}
		})
	}
}

// TestCodexImageGenerationBridge_PreservesClientImageFunctionTools compares
// flat and nested image_gen.imagegen declarations with a similar unrelated
// name. Exact client functions retain instructions and absent tool choice; the
// unrelated name receives hosted-tool bridge injection.
func TestCodexImageGenerationBridge_PreservesClientImageFunctionTools(t *testing.T) {
	tests := []struct {
		name    string         // client image function case
		reqBody map[string]any // mutable bridge request fixture
		// expected client image function recognition
		wantClient bool
	}{
		{
			name: "flat image_gen function",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"input": "draw a cat",
				"tools": []any{
					map[string]any{"type": "function", "name": "image_gen.imagegen"},
				},
			},
			wantClient: true,
		},
		{
			name: "nested image_gen function",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"input": "draw a cat",
				"tools": []any{
					map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": "image_gen.imagegen",
						},
					},
				},
			},
			wantClient: true,
		},
		{
			name: "similar function name still receives hosted bridge",
			reqBody: map[string]any{
				"model": "gpt-5.5",
				"input": "draw a cat",
				"tools": []any{
					map[string]any{"type": "function", "name": "image_gen.imagegenerator"},
				},
			},
			wantClient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.reqBody["instructions"] = "existing instructions"
			require.Equal(t, tt.wantClient, openai.HasCodexImageGenerationFunctionTool(tt.reqBody))

			toolModified := EnsureOpenAIResponsesImageGenerationTool(tt.reqBody)
			choiceModified := EnsureOpenAIResponsesImageGenerationToolChoiceAuto(tt.reqBody)
			instructionsModified := ApplyCodexImageGenerationBridgeInstructions(tt.reqBody)

			require.Equal(t, !tt.wantClient, toolModified)
			require.Equal(t, !tt.wantClient, choiceModified)
			require.Equal(t, !tt.wantClient, instructionsModified)

			hasHostedTool := false
			tools, _ := tt.reqBody["tools"].([]any)
			for _, rawTool := range tools {
				tool, ok := rawTool.(map[string]any)
				if ok && openai.FirstNonEmptyString(tool["type"]) == "image_generation" {
					hasHostedTool = true
				}
			}
			require.Equal(t, !tt.wantClient, hasHostedTool)

			if tt.wantClient {
				require.NotContains(t, tt.reqBody, "tool_choice")
				require.Equal(t, "existing instructions", tt.reqBody["instructions"])
			} else {
				require.Equal(t, "auto", tt.reqBody["tool_choice"])
				require.Contains(t, tt.reqBody["instructions"], openai.CodexImageGenerationBridgeMarker)
			}
		})
	}
}

// TestApplyCodexImageGenerationBridgeInstructions_AppendsBridgeOnce starts
// with existing instructions and a hosted image tool. Bridge guidance appends
// once, retains the prior text, and a second invocation reports no change.
func TestApplyCodexImageGenerationBridgeInstructions_AppendsBridgeOnce(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.4",
		"instructions": "existing instructions",
		"tools": []any{
			map[string]any{"type": "image_generation", "output_format": "png"},
		},
	}

	modified := ApplyCodexImageGenerationBridgeInstructions(reqBody)
	require.True(t, modified)

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Contains(t, instructions, "existing instructions")
	require.Contains(t, instructions, openai.CodexImageGenerationBridgeMarker)
	require.Contains(t, instructions, "Responses native `image_generation` tool")

	modified = ApplyCodexImageGenerationBridgeInstructions(reqBody)
	require.False(t, modified)
}

// TestApplyCodexImageGenerationBridgeInstructions_SkipsSpark supplies a hosted
// image tool on an exact Spark request. The bridge instruction helper reports
// no change and retains the existing instruction text.
func TestApplyCodexImageGenerationBridgeInstructions_SkipsSpark(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.3-codex-spark",
		"instructions": "existing instructions",
		"tools": []any{
			map[string]any{"type": "image_generation", "output_format": "png"},
		},
	}

	modified := ApplyCodexImageGenerationBridgeInstructions(reqBody)
	require.False(t, modified)
	require.Equal(t, "existing instructions", reqBody["instructions"])
}

// TestApplyCodexImageGenerationBridgeInstructions_SkipsWithoutImageTool
// supplies existing instructions with a web-search declaration alone. The
// image bridge helper reports no change and retains the exact instruction
// string.
func TestApplyCodexImageGenerationBridgeInstructions_SkipsWithoutImageTool(t *testing.T) {
	reqBody := map[string]any{
		"instructions": "existing instructions",
		"tools": []any{
			map[string]any{"type": "web_search"},
		},
	}

	modified := ApplyCodexImageGenerationBridgeInstructions(reqBody)
	require.False(t, modified)
	require.Equal(t, "existing instructions", reqBody["instructions"])
}

// TestValidateCodexSparkInputRejectsInputImage supplies Responses text and
// image parts to an exact Spark model. Validation returns an error whose text
// identifies unsupported image input.
func TestValidateCodexSparkInputRejectsInputImage(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"input": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "describe"},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aGVsbG8="},
				},
			},
		},
	}

	err := ValidateCodexSparkInput(reqBody, "gpt-5.3-codex-spark")
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not support image input")
}

// TestValidateCodexSparkInputRejectsChatImageURL supplies a Chat message
// containing text and an image_url object to Spark. Validation rejects that
// alternate image representation with an error.
func TestValidateCodexSparkInputRejectsChatImageURL(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "describe"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="}},
				},
			},
		},
	}

	err := ValidateCodexSparkInput(reqBody, "gpt-5.3-codex-spark")
	require.Error(t, err)
}

// TestValidateCodexSparkInputAllowsTextOnly supplies a Spark request
// containing a text part and no image. Validation returns no error for that
// supported input shape.
func TestValidateCodexSparkInputAllowsTextOnly(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"input": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "hello"},
				},
			},
		},
	}

	require.NoError(t, ValidateCodexSparkInput(reqBody, "gpt-5.3-codex-spark"))
}

// TestApplyCodexOAuthTransform_AddsSparkImageUnsupportedInstructions supplies
// Spark with existing instruction text and plain input. Transformation appends
// Spark-specific image guidance and leaves the ordinary hosted-image bridge
// marker absent.
func TestApplyCodexOAuthTransform_AddsSparkImageUnsupportedInstructions(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.3-codex-spark",
		"instructions": "existing instructions",
		"input":        "hello",
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false)
	require.True(t, result.Modified)

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Contains(t, instructions, "existing instructions")
	require.Contains(t, instructions, openai.CodexSparkImageUnsupportedMarker)
	require.Contains(t, instructions, "does not support image generation")
	require.Contains(t, instructions, "switch to a non-Spark Codex model")
	require.NotContains(t, instructions, openai.CodexImageGenerationBridgeMarker)
}

// TestApplyCodexOAuthTransform_DoesNotAddSparkImageUnsupportedForNonSpark uses
// ordinary GPT text input and existing instructions. The resulting
// instructions do not contain the Spark-only image capability marker.
func TestApplyCodexOAuthTransform_DoesNotAddSparkImageUnsupportedForNonSpark(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.4",
		"instructions": "existing instructions",
		"input":        "hello",
	}

	ApplyCodexOAuthTransform(reqBody, true, false)
	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.NotContains(t, instructions, openai.CodexSparkImageUnsupportedMarker)
}

// TestApplyCodexOAuthTransform_StripsImageGenerationToolForSpark supplies
// Spark with function and hosted image tools. Transformation reports
// modification, removes image-generation capability, and retains the named
// function declaration.
func TestApplyCodexOAuthTransform_StripsImageGenerationToolForSpark(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"input": "hello",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "image_generation", "output_format": "png"},
		},
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false)
	require.True(t, result.Modified)
	require.False(t, openai.HasOpenAIImageGenerationTool(reqBody))

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	first, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function", first["type"])
	require.Equal(t, "shell", first["name"])
}

// TestApplyCodexOAuthTransform_StripsImageGenerationToolForSparkAlias supplies
// the exact Spark model with a sole hosted image tool. Transformation removes
// image capability and omits the now-empty tools field.
func TestApplyCodexOAuthTransform_StripsImageGenerationToolForSparkAlias(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"input": "hello",
		"tools": []any{
			map[string]any{"type": "image_generation", "output_format": "png"},
		},
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false)
	require.True(t, result.Modified)
	require.False(t, openai.HasOpenAIImageGenerationTool(reqBody))
	// An empty tool list is omitted.
	_, hasTools := reqBody["tools"]
	require.False(t, hasTools)
}

// TestStripOpenAIImageGenerationTools_StripsNamespaceFormats mixes hosted
// image namespaces with code tools in top-level and additional_tools lists.
// Stripping retains code declarations and messages, removes image selection,
// and reports no change when repeated.
func TestStripOpenAIImageGenerationTools_StripsNamespaceFormats(t *testing.T) {
	imageNamespace := func() map[string]any {
		return map[string]any{
			"type": "namespace",
			"name": "image_gen",
			"tools": []any{
				map[string]any{"type": "function", "name": "imagegen"},
			},
		}
	}
	codeNamespace := func() map[string]any {
		return map[string]any{
			"type": "namespace",
			"name": "code_tools",
			"tools": []any{
				map[string]any{"type": "function", "name": "run"},
			},
		}
	}

	reqBody := map[string]any{
		"model": "gpt-5.5",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			imageNamespace(),
			codeNamespace(),
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{imageNamespace(), codeNamespace()},
			},
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{imageNamespace()},
			},
		},
		"tool_choice": map[string]any{"type": "namespace", "name": "image_gen"},
	}

	require.True(t, openai.StripOpenAIImageGenerationTools(reqBody))
	require.False(t, openai.HasOpenAIImageGenerationTool(reqBody))
	require.NotContains(t, reqBody, "tool_choice")

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 2)
	firstTool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	secondTool, ok := tools[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "shell", firstTool["name"])
	require.Equal(t, "code_tools", secondTool["name"])

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	message, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "message", message["type"])
	additionalToolsItem, ok := input[1].(map[string]any)
	require.True(t, ok)
	additionalTools, ok := additionalToolsItem["tools"].([]any)
	require.True(t, ok)
	require.Len(t, additionalTools, 1)
	additionalTool, ok := additionalTools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "code_tools", additionalTool["name"])
	require.False(t, openai.StripOpenAIImageGenerationTools(reqBody), "stripping should be idempotent")
}

// TestStripOpenAIImageGenerationTools_KeepsNonImageNamespaces supplies
// code/browser namespaces and auto selection with no image declarations.
// Stripping reports false and retains the automatic tool choice.
func TestStripOpenAIImageGenerationTools_KeepsNonImageNamespaces(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{
			map[string]any{"type": "namespace", "name": "code_tools"},
		},
		"input": []any{
			map[string]any{
				"type": "additional_tools",
				"tools": []any{
					map[string]any{"type": "namespace", "name": "browser_tools"},
				},
			},
		},
		"tool_choice": "auto",
	}

	require.False(t, openai.StripOpenAIImageGenerationTools(reqBody))
	require.Equal(t, "auto", reqBody["tool_choice"])
	require.False(t, openai.HasOpenAIImageGenerationTool(reqBody))
}

// TestStripOpenAIImageGenerationTools_KeepsCustomImagegenFunctionChoice
// supplies a custom nested function choice named imagegen with no image tool
// declaration. Stripping reports no change and retains tool_choice.
func TestStripOpenAIImageGenerationTools_KeepsCustomImagegenFunctionChoice(t *testing.T) {
	reqBody := map[string]any{
		"tool_choice": map[string]any{
			"function": map[string]any{"name": "imagegen"},
		},
	}

	require.False(t, openai.StripOpenAIImageGenerationTools(reqBody))
	require.Contains(t, reqBody, "tool_choice")
}

// TestApplyCodexOAuthTransform_KeepsImageGenerationToolForNonSpark supplies a
// hosted image-generation tool on a non-Spark Codex model with plain input.
// The transformed request continues to advertise that image-generation
// capability.
func TestApplyCodexOAuthTransform_KeepsImageGenerationToolForNonSpark(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex",
		"input": "hello",
		"tools": []any{
			map[string]any{"type": "image_generation", "output_format": "png"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, true, false)
	require.True(t, openai.HasOpenAIImageGenerationTool(reqBody))
}

// TestNormalizeOpenAIResponsesImageOnlyModel_BuildsImageToolRequest supplies
// an image-only model with prompt and top-level image options. Normalization
// selects the text driver, moves prompt to input and options into the image
// tool, and selects image generation.
func TestNormalizeOpenAIResponsesImageOnlyModel_BuildsImageToolRequest(t *testing.T) {
	reqBody := map[string]any{
		"model":         "gpt-image-2",
		"prompt":        "draw a cat",
		"size":          "1024x1024",
		"output_format": "png",
	}

	modified := openai.NormalizeOpenAIResponsesImageOnlyModel(reqBody, true)
	require.True(t, modified)
	require.Equal(t, openai.ImagesResponsesMainModel, reqBody["model"])
	require.Equal(t, "draw a cat", reqBody["input"])
	_, hasPrompt := reqBody["prompt"]
	require.False(t, hasPrompt)
	_, hasTopLevelSize := reqBody["size"]
	require.False(t, hasTopLevelSize)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image_generation", tool["type"])
	require.Equal(t, "gpt-image-2", tool["model"])
	require.Equal(t, "1024x1024", tool["size"])
	require.Equal(t, "png", tool["output_format"])

	choice, ok := reqBody["tool_choice"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image_generation", choice["type"])
}

// TestNormalizeOpenAIResponsesImageOnlyModel_PreservesExistingImageTool
// supplies an image-only model plus a hosted tool naming a different image
// model. Normalization selects the text driver while retaining the existing
// tool model and auto choice.
func TestNormalizeOpenAIResponsesImageOnlyModel_PreservesExistingImageTool(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-image-2",
		"input": "draw a cat",
		"tools": []any{
			map[string]any{
				"type":  "image_generation",
				"model": "gpt-image-1.5",
			},
		},
		"tool_choice": "auto",
	}

	modified := openai.NormalizeOpenAIResponsesImageOnlyModel(reqBody, true)
	require.True(t, modified)
	require.Equal(t, openai.ImagesResponsesMainModel, reqBody["model"])
	require.Equal(t, "auto", reqBody["tool_choice"])

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-image-1.5", tool["model"])
}

// TestValidateOpenAIResponsesImageModel_RejectsImageOnlyModel uses an
// image-only top-level model with a hosted image tool. Validation returns the
// error requiring a Responses-capable text driver for that request.
func TestValidateOpenAIResponsesImageModel_RejectsImageOnlyModel(t *testing.T) {
	err := openai.ValidateOpenAIResponsesImageModel(map[string]any{
		"tools": []any{
			map[string]any{"type": "image_generation"},
		},
	}, "gpt-image-2", true)

	require.ErrorContains(t, err, `/v1/responses image_generation requests require a Responses-capable text model`)
}

// TestApplyCodexOAuthTransform_EmptyInput supplies an allocated empty input
// array alongside the GPT model in an OAuth request. The resulting input
// retains array representation and zero items.
func TestApplyCodexOAuthTransform_EmptyInput(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.1",
		"input": []any{},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 0)
}

// TestCodexTransformPreservesModelID passes version spellings, vendor
// prefixes, effort suffixes, and aliases through OAuth transformation. Each
// full model identifier stays unchanged in the request body.
func TestCodexTransformPreservesModelID(t *testing.T) {
	cases := map[string]string{
		"gpt-5.4":                   "gpt-5.4",
		"gpt5.5":                    "gpt5.5",
		"openai/gpt5.5":             "openai/gpt5.5",
		"gpt-5.5-pro":               "gpt-5.5-pro",
		"gpt5.5-pro":                "gpt5.5-pro",
		"openai/gpt5.5-pro":         "openai/gpt5.5-pro",
		"gpt-5.5-pro-high":          "gpt-5.5-pro-high",
		"gpt-5.5-max":               "gpt-5.5-max",
		"gpt5.6sol":                 "gpt5.6sol",
		"gpt-5.6-sol-max":           "gpt-5.6-sol-max",
		"gpt-5.6-terra-ultra":       "gpt-5.6-terra-ultra",
		"openai/gpt5.6luna-high":    "openai/gpt5.6luna-high",
		"gpt-5.6-luna-max":          "gpt-5.6-luna-max",
		"gpt-5.6-luna-ultra":        "gpt-5.6-luna-ultra",
		"codex-auto-review":         "codex-auto-review",
		"gpt5.4":                    "gpt5.4",
		"gpt-5.4-high":              "gpt-5.4-high",
		"gpt-5.4-chat-latest":       "gpt-5.4-chat-latest",
		"gpt 5.4":                   "gpt 5.4",
		"gpt-5.4-mini":              "gpt-5.4-mini",
		"gpt5.4-mini":               "gpt5.4-mini",
		"gpt5.4mini":                "gpt5.4mini",
		"gpt 5.4 mini":              "gpt 5.4 mini",
		"gpt-5.3":                   "gpt-5.3",
		"gpt5.3":                    "gpt5.3",
		"gpt-5.3-codex":             "gpt-5.3-codex",
		"gpt5.3-codex":              "gpt5.3-codex",
		"gpt5.3codex":               "gpt5.3codex",
		"gpt-5.3-codex-xhigh":       "gpt-5.3-codex-xhigh",
		"gpt-5.3-codex-spark":       "gpt-5.3-codex-spark",
		"gpt5.3-codex-spark":        "gpt5.3-codex-spark",
		"gpt5.3codexspark":          "gpt5.3codexspark",
		"gpt 5.3 codex spark":       "gpt 5.3 codex spark",
		"gpt-5.3-codex-spark-high":  "gpt-5.3-codex-spark-high",
		"gpt-5.3-codex-spark-xhigh": "gpt-5.3-codex-spark-xhigh",
		"gpt 5.3 codex":             "gpt 5.3 codex",
	}

	for input, expected := range cases {
		body := map[string]any{"model": input}
		ApplyCodexOAuthTransform(body, false, false)
		require.Equal(t, expected, body["model"])
	}
}

// TestCodexTransformPreservesLegacyModelID passes older Codex identifiers and
// an empty model field through transformation. Their values remain unchanged
// for the listed cases.
func TestCodexTransformPreservesLegacyModelID(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"gpt-5":              "gpt-5",
		"gpt-5-mini":         "gpt-5-mini",
		"gpt-5-nano":         "gpt-5-nano",
		"gpt-5.1":            "gpt-5.1",
		"gpt-5.1-codex":      "gpt-5.1-codex",
		"gpt-5.1-codex-max":  "gpt-5.1-codex-max",
		"gpt-5.1-codex-mini": "gpt-5.1-codex-mini",
		"gpt-5.2-codex":      "gpt-5.2-codex",
		"codex-mini-latest":  "codex-mini-latest",
		"gpt-5-codex":        "gpt-5-codex",
	}

	for input, expected := range cases {
		body := map[string]any{"model": input}
		ApplyCodexOAuthTransform(body, false, false)
		require.Equal(t, expected, body["model"])
	}
}

// TestApplyCodexOAuthTransform_PreservesBareSparkModel supplies an exact Spark
// model and empty input. Both request and returned normalized model retain the
// exact identifier, while storage is set false.
func TestApplyCodexOAuthTransform_PreservesBareSparkModel(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex-spark",
		"input": []any{},
	}

	result := ApplyCodexOAuthTransform(reqBody, false, false)

	require.Equal(t, "gpt-5.3-codex-spark", reqBody["model"])
	require.Equal(t, "gpt-5.3-codex-spark", result.NormalizedModel)
	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)
}

// TestApplyCodexOAuthTransform_TrimmedModelWithoutPolicyRewrite supplies a
// padded exact Spark identifier with empty input. Transformation trims both
// request model and returned normalized identity and reports the request
// modification.
func TestApplyCodexOAuthTransform_TrimmedModelWithoutPolicyRewrite(t *testing.T) {
	reqBody := map[string]any{
		"model": "  gpt-5.3-codex-spark  ",
		"input": []any{},
	}

	result := ApplyCodexOAuthTransform(reqBody, false, false)

	require.Equal(t, "gpt-5.3-codex-spark", reqBody["model"])
	require.Equal(t, "gpt-5.3-codex-spark", result.NormalizedModel)
	require.True(t, result.Modified)
}

// TestApplyCodexOAuthTransform_CodexCLI_PreservesExistingInstructions supplies
// instruction text with CLI mode enabled. The transformed instructions retain
// the exact existing text, independently of other fields that may mark the
// request modified.
func TestApplyCodexOAuthTransform_CodexCLI_PreservesExistingInstructions(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.1",
		"instructions": "existing instructions",
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false) // isCodexCLI=true

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Equal(t, "existing instructions", instructions)
	// Other transformations may set Modified even when instructions remain unchanged.
	_ = result
}

// TestApplyCodexOAuthTransform_CodexCLI_SuppliesDefaultWhenEmpty omits
// instructions on a CLI-mode request. Transformation creates a nonempty
// instruction string and reports that the request was modified.
func TestApplyCodexOAuthTransform_CodexCLI_SuppliesDefaultWhenEmpty(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.1",
		// Instructions are absent.
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false) // isCodexCLI=true

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.NotEmpty(t, instructions)
	require.True(t, result.Modified)
}

// TestApplyCodexOAuthTransform_GPT55SuppliesModelSpecificInstructions supplies
// blank instructions for GPT-5.5. Transformation uses the GPT-5 coding-agent
// text and excludes the older GPT-5.1 CLI instruction phrase.
func TestApplyCodexOAuthTransform_GPT55SuppliesModelSpecificInstructions(t *testing.T) {
	reqBody := map[string]any{
		"model":        "gpt-5.5",
		"instructions": "   ",
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false)

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Contains(t, instructions, "You are Codex, a coding agent based on GPT-5")
	require.NotContains(t, instructions, "You are GPT-5.1 running in the Codex CLI")
	require.True(t, result.Modified)
}

// TestApplyCodexOAuthTransform_NonCodexCLI_PreservesExistingInstructions
// supplies a model and nonempty instruction text with CLI mode disabled.
// Transformation retains the exact instruction string on the resulting non-CLI
// request.
func TestApplyCodexOAuthTransform_NonCodexCLI_PreservesExistingInstructions(t *testing.T) {
	// Non-CLI requests also retain client instructions.

	reqBody := map[string]any{
		"model":        "gpt-5.1",
		"instructions": "old instructions",
	}

	ApplyCodexOAuthTransform(reqBody, false, false) // isCodexCLI=false

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Equal(t, "old instructions", instructions)
}

// TestApplyCodexOAuthTransform_StringInputConvertedToArray supplies a plain
// string input to OAuth transformation. The resulting one-item array contains
// a user message with the original text and reports modification.
func TestApplyCodexOAuthTransform_StringInputConvertedToArray(t *testing.T) {
	reqBody := map[string]any{"model": "gpt-5.4", "input": "Hello, world!"}
	result := ApplyCodexOAuthTransform(reqBody, false, false)
	require.True(t, result.Modified)
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	msg, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "message", msg["type"])
	require.Equal(t, "user", msg["role"])
	require.Equal(t, "Hello, world!", msg["content"])
}

// TestApplyCodexOAuthTransform_EmptyStringInputBecomesEmptyArray supplies an
// empty string as input on a model-bearing OAuth request. Transformation
// reports modification and returns an allocated input array with zero items.
func TestApplyCodexOAuthTransform_EmptyStringInputBecomesEmptyArray(t *testing.T) {
	reqBody := map[string]any{"model": "gpt-5.4", "input": ""}
	result := ApplyCodexOAuthTransform(reqBody, false, false)
	require.True(t, result.Modified)
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 0)
}

// TestApplyCodexOAuthTransform_WhitespaceStringInputBecomesEmptyArray supplies
// whitespace-only input alongside a model identifier in an OAuth request.
// Transformation reports modification and creates a zero-item input array.
func TestApplyCodexOAuthTransform_WhitespaceStringInputBecomesEmptyArray(t *testing.T) {
	reqBody := map[string]any{"model": "gpt-5.4", "input": "   "}
	result := ApplyCodexOAuthTransform(reqBody, false, false)
	require.True(t, result.Modified)
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 0)
}

// TestApplyCodexOAuthTransform_StringInputWithToolsField supplies string input
// alongside a declared function tool. Transformation converts the input to a
// one-item array in that request.
func TestApplyCodexOAuthTransform_StringInputWithToolsField(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": "Run the tests",
		"tools": []any{map[string]any{"type": "function", "name": "bash"}},
	}
	ApplyCodexOAuthTransform(reqBody, false, false)
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
}

// TestExtractSystemMessagesFromInput exercises absent, string, array,
// multiple, and mixed system messages with optional promoted-message omission.
// Assertions cover instruction ordering, developer-role conversion, retained
// user content, and mixed image content that remains in input.
func TestExtractSystemMessagesFromInput(t *testing.T) {
	t.Run("no system messages", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"role": "user", "content": "hello"},
			},
		}
		result := openai.ExtractSystemMessagesFromInput(reqBody, false)
		require.False(t, result)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		_, hasInstructions := reqBody["instructions"]
		require.False(t, hasInstructions)
	})

	t.Run("string content system message", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"role": "system", "content": "You are an assistant."},
				map[string]any{"role": "user", "content": "hello"},
			},
		}
		result := openai.ExtractSystemMessagesFromInput(reqBody, false)
		require.True(t, result)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 2)
		msg, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", msg["role"])
		require.Equal(t, "You are an assistant.", msg["content"])
		user, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "user", user["role"])
		require.Equal(t, "hello", user["content"])
		require.Equal(t, "You are an assistant.", reqBody["instructions"])
	})

	t.Run("array content system message", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{
					"role": "system",
					"content": []any{
						map[string]any{"type": "text", "text": "Be helpful."},
					},
				},
			},
		}
		result := openai.ExtractSystemMessagesFromInput(reqBody, false)
		require.True(t, result)
		require.Equal(t, "Be helpful.", reqBody["instructions"])
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		msg, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", msg["role"])
	})

	t.Run("multiple system messages concatenated", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"role": "system", "content": "First."},
				map[string]any{"role": "system", "content": "Second."},
				map[string]any{"role": "user", "content": "hi"},
			},
		}
		result := openai.ExtractSystemMessagesFromInput(reqBody, false)
		require.True(t, result)
		require.Equal(t, "First.\n\nSecond.", reqBody["instructions"])
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 3)
		first, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", first["role"])
		second, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", second["role"])
		user, ok := input[2].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "user", user["role"])
	})

	t.Run("mixed system and non-system preserves non-system", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"role": "user", "content": "hello"},
				map[string]any{"role": "system", "content": "Sys prompt."},
				map[string]any{"role": "assistant", "content": "Hi there"},
			},
		}
		result := openai.ExtractSystemMessagesFromInput(reqBody, false)
		require.True(t, result)
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 3)
		first, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "user", first["role"])
		second, ok := input[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", second["role"])
		third, ok := input[2].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "assistant", third["role"])
		require.Equal(t, "Sys prompt.", reqBody["instructions"])
	})

	t.Run("existing instructions prepended", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"role": "system", "content": "Extracted."},
				map[string]any{"role": "user", "content": "hi"},
			},
			"instructions": "Existing instructions.",
		}
		result := openai.ExtractSystemMessagesFromInput(reqBody, false)
		require.True(t, result)
		require.Equal(t, "Extracted.\n\nExisting instructions.", reqBody["instructions"])
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		msg, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", msg["role"])
	})

	t.Run("omit losslessly promoted text-only messages", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{"role": "system", "content": "First."},
				map[string]any{
					"role": "system",
					"content": []any{
						map[string]any{"type": "text", "text": "Second "},
						map[string]any{"type": "input_text", "text": "and "},
						map[string]any{"type": "output_text", "text": "third."},
					},
				},
				map[string]any{"role": "user", "content": "hi"},
			},
			"instructions": "Existing.",
		}

		result := openai.ExtractSystemMessagesFromInput(reqBody, true)

		require.True(t, result)
		require.Equal(t, "First.\n\nSecond and third.\n\nExisting.", reqBody["instructions"])
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 1)
		user, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "user", user["role"])
	})

	t.Run("omit keeps mixed system content as developer", func(t *testing.T) {
		reqBody := map[string]any{
			"input": []any{
				map[string]any{
					"role": "system",
					"content": []any{
						map[string]any{"type": "input_text", "text": "Inspect this image."},
						map[string]any{"type": "input_image", "image_url": "https://example.com/image.png"},
					},
				},
				map[string]any{"role": "user", "content": "hi"},
			},
		}

		result := openai.ExtractSystemMessagesFromInput(reqBody, true)

		require.True(t, result)
		require.Equal(t, "Inspect this image.", reqBody["instructions"])
		input, ok := reqBody["input"].([]any)
		require.True(t, ok)
		require.Len(t, input, 2)
		developer, ok := input[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "developer", developer["role"])
		require.Len(t, developer["content"], 2)
	})
}

// TestApplyCodexOAuthTransform_StripsPromptCacheRetention supplies the
// unsupported prompt_cache_retention field with a user message in an OAuth
// request. Transformation removes that field from the resulting request body.
func TestApplyCodexOAuthTransform_StripsPromptCacheRetention(t *testing.T) {
	reqBody := map[string]any{
		"model":                  "gpt-5.1",
		"prompt_cache_retention": "24h",
		"input": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}

	ApplyCodexOAuthTransform(reqBody, false, false)

	_, stillThere := reqBody["prompt_cache_retention"]
	require.False(t, stillThere,
		"prompt_cache_retention must be stripped before forwarding to Codex upstream")
}

// TestApplyCodexOAuthTransform_StripsChatGPTInternalUnsupportedFields supplies
// every listed ChatGPT-internal unsupported field with user input.
// Transformation reports modification and removes all fields in the upstream
// unsupported-field list.
func TestApplyCodexOAuthTransform_StripsChatGPTInternalUnsupportedFields(t *testing.T) {
	reqBody := map[string]any{
		"model":                  "gpt-5.4",
		"chat_template_kwargs":   map[string]any{"enable_thinking": true},
		"user":                   "user_123",
		"metadata":               map[string]any{"trace_id": "abc"},
		"prompt_cache_retention": "24h",
		"safety_identifier":      "sid",
		"stream_options":         map[string]any{"include_usage": true},
		"truncation":             "auto",
		"stop_sequences":         []any{"END"},
		"input": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false)

	require.True(t, result.Modified)
	for _, field := range openai.OpenAIChatGPTInternalUnsupportedFields {
		require.NotContains(t, reqBody, field)
	}
}

// TestApplyCodexOAuthTransform_NormalizesPromptAndCommands supplies legacy
// prompt text and commands on a Codex request. Transformation creates a
// user-message input array and removes both legacy fields.
func TestApplyCodexOAuthTransform_NormalizesPromptAndCommands(t *testing.T) {
	reqBody := map[string]any{
		"model":    "gpt-5.5",
		"prompt":   "hello",
		"commands": []any{"unsupported"},
	}

	result := ApplyCodexOAuthTransform(reqBody, true, false)
	require.True(t, result.Modified)
	require.Equal(t, []any{
		map[string]any{"type": "message", "role": "user", "content": "hello"},
	}, reqBody["input"])
	require.NotContains(t, reqBody, "prompt")
	require.NotContains(t, reqBody, "commands")
}

// TestNormalizeOpenAIResponsesImageGenerationTools_PreservesInputFidelityBeforeResolution
// supplies input_fidelity on two image tool models before final attribute
// resolution. General tool normalization reports false and retains both
// fidelity values.
func TestNormalizeOpenAIResponsesImageGenerationTools_PreservesInputFidelityBeforeResolution(t *testing.T) {
	reqBody := map[string]any{"tools": []any{
		map[string]any{"type": "image_generation", "model": "gpt-image-2-codex", "input_fidelity": "high"},
		map[string]any{"type": "image_generation", "model": "gpt-image-1.5", "input_fidelity": "high"},
	}}

	require.False(t, openai.NormalizeOpenAIResponsesImageGenerationTools(reqBody))
	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 2)
	first, ok := tools[0].(map[string]any)
	require.True(t, ok)
	second, ok := tools[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "high", first["input_fidelity"])
	require.Equal(t, "high", second["input_fidelity"])
}

// TestOpenAIRequestBodyImageGenerationToolNeedsNormalization_GPTImage2InputFidelity
// supplies a GPT-image-2 Codex hosted tool with input_fidelity set high. The
// general image-tool normalization predicate reports that this request body
// needs no rewrite.
func TestOpenAIRequestBodyImageGenerationToolNeedsNormalization_GPTImage2InputFidelity(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation","model":"gpt-image-2-codex","input_fidelity":"high"}]}`)

	require.False(t, ImageIntent().OpenAIRequestBodyImageGenerationToolNeedsNormalization(body))
}

// TestApplyCodexOAuthTransform_ExtractsSystemMessages supplies system and user
// input messages to the full transformation. System text appears in
// instructions and a developer message, while the user role remains unchanged.
func TestApplyCodexOAuthTransform_ExtractsSystemMessages(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.1",
		"input": []any{
			map[string]any{"role": "system", "content": "You are a coding assistant."},
			map[string]any{"role": "user", "content": "Write a function."},
		},
	}

	result := ApplyCodexOAuthTransform(reqBody, false, false)

	require.True(t, result.Modified)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	system, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "developer", system["role"])
	require.Equal(t, "You are a coding assistant.", system["content"])
	user, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "user", user["role"])

	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Equal(t, "You are a coding assistant.", instructions)
}

// TestApplyCodexOAuthTransform_JsonObjectKeepsJsonInstructionInInput requests
// JSON-object output with JSON guidance in the system message. Transformation
// retains that guidance both in instructions and developer input alongside the
// user message.
func TestApplyCodexOAuthTransform_JsonObjectKeepsJsonInstructionInInput(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.4",
		"input": []any{
			map[string]any{
				"role":    "system",
				"content": "You are an assistant. Output JSON only.",
			},
			map[string]any{
				"role":    "user",
				"content": "symbol data without the keyword",
			},
		},
		"text": map[string]any{
			"format": map[string]any{
				"type": "json_object",
			},
		},
	}

	result := ApplyCodexOAuthTransform(reqBody, false, false)

	require.True(t, result.Modified)
	instructions, ok := reqBody["instructions"].(string)
	require.True(t, ok)
	require.Contains(t, instructions, "JSON")
	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	developer, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "developer", developer["role"])
	require.Contains(t, developer["content"], "JSON")
	user, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "user", user["role"])
}

// TestIsInstructionsEmpty covers absent, nil, blank, padded, non-string, and
// nonempty instruction values. The predicate treats only the nonblank string
// case as present instructions.
func TestIsInstructionsEmpty(t *testing.T) {
	tests := []struct {
		name     string         // instruction emptiness case label
		reqBody  map[string]any // instruction value request fixture
		expected bool           // expected instruction absence result
	}{
		{"missing field", map[string]any{}, true},
		{"nil value", map[string]any{"instructions": nil}, true},
		{"empty string", map[string]any{"instructions": ""}, true},
		{"whitespace only", map[string]any{"instructions": "   "}, true},
		{"non-string", map[string]any{"instructions": 123}, true},
		{"valid string", map[string]any{"instructions": "hello"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := openai.IsInstructionsEmpty(tt.reqBody)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestFilterCodexInput_PreservesReasoningStripsID runs reference preservation
// both ways for reasoning with encrypted content and call_id. Filtering
// retains encrypted content and empty summary while removing item and non-pair
// call IDs.
func TestFilterCodexInput_PreservesReasoningStripsID(t *testing.T) {
	build := func() []any {
		return []any{
			map[string]any{
				"type":              "reasoning",
				"id":                "rs_0672f12450da0b9c0169f07220a6c08198b68c2455ced99344",
				"call_id":           "remove_non_pair_call_id",
				"encrypted_content": "gAAAAAB-enc-payload",
				"summary":           []any{},
			},
		}
	}

	for _, preserve := range []bool{true, false} {
		t.Run(fmt.Sprintf("preserveReferences=%v", preserve), func(t *testing.T) {
			filtered := openai.FilterCodexInputWithOptions(build(), openai.CodexInputFilterOptions{PreserveReferences: preserve})
			require.Len(t, filtered, 1)

			item, ok := filtered[0].(map[string]any)
			require.True(t, ok)

			require.Equal(t, "reasoning", item["type"])

			require.Equal(t, "gAAAAAB-enc-payload", item["encrypted_content"])
			// Reference preservation retains encrypted replay data while discarding its item ID.
			// This assertion checks transformation output; it does not contact upstream lookup.
			_, hasID := item["id"]
			require.False(t, hasID)
			_, hasCallID := item["call_id"]
			require.False(t, hasCallID)

			summary, ok := item["summary"].([]any)
			require.True(t, ok)
			require.Len(t, summary, 0)
		})
	}
}

// TestFilterCodexInput_BareReasoningStripsIDBackfillsSummary supplies
// reasoning that already has an empty summary among message and tool history.
// Filtering retains that summary and all four items while removing the
// reasoning ID.
func TestFilterCodexInput_BareReasoningStripsIDBackfillsSummary(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "id": "msg_0", "role": "user", "content": "hi"},
		map[string]any{
			"type":    "reasoning",
			"id":      "rs_0672f12450da0b9c0169f07220a6c08198b68c2455ced99344",
			"summary": []any{},
		},
		map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "tool"},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "{}"},
	}

	filtered := openai.FilterCodexInputWithOptions(input, openai.CodexInputFilterOptions{PreserveReferences: false})

	require.Len(t, filtered, 4)
	reasoning, ok := filtered[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "reasoning", reasoning["type"])
	require.Equal(t, []any{}, reasoning["summary"])
	require.NotContains(t, reasoning, "id")
}

// TestFilterCodexInput_ReasoningBackfillsMissingSummary supplies encrypted
// reasoning without a summary. Filtering adds an empty summary, retains
// encrypted content, and removes the replay item ID.
func TestFilterCodexInput_ReasoningBackfillsMissingSummary(t *testing.T) {
	// Reasoning requires a summary; supply an empty array while retaining encrypted content.
	input := []any{
		map[string]any{
			"type":              "reasoning",
			"id":                "rs_0672f12450da0b9c0169f07220a6c08198b68c2455ced99344",
			"encrypted_content": "gAAAAAB-enc-payload",
		},
	}

	filtered := openai.FilterCodexInputWithOptions(input, openai.CodexInputFilterOptions{PreserveReferences: false})

	require.Len(t, filtered, 1)
	reasoning, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "reasoning", reasoning["type"])
	require.Equal(t, []any{}, reasoning["summary"])
	require.Equal(t, "gAAAAAB-enc-payload", reasoning["encrypted_content"])
	require.NotContains(t, reasoning, "id")
}

// TestFilterCodexInput_PreservesReasoningSummaryContentAndEncryptedContent
// supplies reasoning summary, plaintext content, and encrypted content with
// reference preservation enabled. Those values survive while the reasoning
// item ID is removed.
func TestFilterCodexInput_PreservesReasoningSummaryContentAndEncryptedContent(t *testing.T) {
	// Reference preservation applies to item_reference entries. This reasoning
	// object keeps its content fields while its own replay ID is removed.
	summary := []any{map[string]any{"type": "summary_text", "text": "checked files"}}
	content := []any{map[string]any{"type": "reasoning_text", "text": "private state"}}
	input := []any{
		map[string]any{
			"type":              "reasoning",
			"id":                "rs_0672f12450da0b9c0169f07220a6c08198b68c2455ced99344",
			"summary":           summary,
			"content":           content,
			"encrypted_content": "gAAAAAB-enc-payload",
		},
	}

	filtered := openai.FilterCodexInputWithOptions(input, openai.CodexInputFilterOptions{PreserveReferences: true})

	require.Len(t, filtered, 1)
	reasoning, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, summary, reasoning["summary"])
	require.Equal(t, content, reasoning["content"])
	require.Equal(t, "gAAAAAB-enc-payload", reasoning["encrypted_content"])
	require.NotContains(t, reasoning, "id")
}

// TestFilterCodexInput_StripsReasoningIDsWhenReferencesArePreserved enables
// reference preservation for a reasoning item with an ID and summary. The item
// survives but its replay ID is removed.
func TestFilterCodexInput_StripsReasoningIDsWhenReferencesArePreserved(t *testing.T) {
	// Even when references are preserved, reasoning's own ID follows the removal
	// rule; the reasoning item itself remains in the retained input.
	input := []any{
		map[string]any{
			"type":    "reasoning",
			"id":      "rs_0672f12450da0b9c0169f07220a6c08198b68c2455ced99344",
			"summary": []any{},
		},
	}

	filtered := openai.FilterCodexInputWithOptions(input, openai.CodexInputFilterOptions{PreserveReferences: true})

	require.Len(t, filtered, 1)
	reasoning, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "reasoning", reasoning["type"])
	require.NotContains(t, reasoning, "id")
}

// TestApplyCodexClientMetadata supplies an OpenAI OAuth device ID with absent
// or partial metadata. Installation ID injection retains unrelated metadata,
// remains idempotent, and skips an OAuth provider lacking a device ID.
func TestApplyCodexClientMetadata(t *testing.T) {
	// GetOpenAIDeviceID exposes the device ID only for OpenAI OAuth providers.
	acc := &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Extra: map[string]any{"openai_device_id": "dev-xyz"}}}

	body := map[string]any{}
	require.True(t, ApplyCodexClientMetadata(body, acc))
	cm, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "dev-xyz", cm["x-codex-installation-id"])
	// Repeated injection is a no-op.
	require.False(t, ApplyCodexClientMetadata(body, acc))

	// An OAuth provider without device_id supplies no installation key.
	body2 := map[string]any{}
	require.False(t, ApplyCodexClientMetadata(body2, &ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}}))
	_, ok = body2["client_metadata"]
	require.False(t, ok)

	// Retain existing client metadata and add the missing installation key.
	body3 := map[string]any{"client_metadata": map[string]any{"x-codex-turn-metadata": "t"}}
	require.True(t, ApplyCodexClientMetadata(body3, acc))
	cm3, _ := body3["client_metadata"].(map[string]any)
	require.Equal(t, "t", cm3["x-codex-turn-metadata"])
	require.Equal(t, "dev-xyz", cm3["x-codex-installation-id"])
}
