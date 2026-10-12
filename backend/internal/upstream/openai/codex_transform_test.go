package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// TestFilterCodexInput_StripsFunctionCallItemID_WhenPreservingReferences covers item_* IDs
// that upstream rejects because function_call IDs require the fc prefix (issue #3785).
func TestFilterCodexInput_StripsFunctionCallItemID_WhenPreservingReferences(t *testing.T) {
	input := []any{
		map[string]any{
			"type":    "function_call",
			"id":      "item_A9v0SNfS3VaLrfX0j3y4xhyK",
			"call_id": "fc_abc123",
			"name":    "bash",
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "fc_abc123",
			"output":  "done",
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 2)

	fc, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function_call", fc["type"])
	_, hasID := fc["id"]
	require.False(t, hasID, "item_* id should be stripped from function_call")
	require.Equal(t, "fc_abc123", fc["call_id"], "call_id must be preserved")
	require.Equal(t, "bash", fc["name"])
}

// TestFilterCodexInput_KeepsFcID_WhenPreservingReferences verifies valid fc IDs survive continuation.
func TestFilterCodexInput_KeepsFcID_WhenPreservingReferences(t *testing.T) {
	input := []any{
		map[string]any{
			"type":    "function_call",
			"id":      "fc_validID123",
			"call_id": "fc_validID123",
			"name":    "bash",
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 1)
	fc, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_validID123", fc["id"], "valid fc* id must be preserved")
}

func TestFilterCodexInput_PreservesNativeCustomAndToolSearchIDs(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "id": "ctc_valid", "call_id": "call_custom", "name": "apply_patch"},
		map[string]any{"type": "tool_search_call", "id": "tsc_valid", "call_id": "call_search"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "ctc_valid", testassert.MustType[map[string]any](filtered[0])["id"])
	require.Equal(t, "tsc_valid", testassert.MustType[map[string]any](filtered[1])["id"])
}

func TestFilterCodexInput_StripsWrongCustomAndToolSearchIDs(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "id": "fc_wrong", "call_id": "call_custom", "name": "apply_patch"},
		map[string]any{"type": "tool_search_call", "id": "fc_wrong", "call_id": "call_search"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.NotContains(t, testassert.MustType[map[string]any](filtered[0]), "id")
	require.NotContains(t, testassert.MustType[map[string]any](filtered[1]), "id")
}

func TestFilterCodexInput_MapsItemReferencesToNativeToolCallPair(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "id": "fc_custom", "call_id": "call_custom", "name": "apply_patch"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "fc_custom", "output": "done"},
		map[string]any{"type": "item_reference", "id": "call_custom"},
		map[string]any{"type": "tool_search_call", "id": "fc_search", "call_id": "call_search"},
		map[string]any{"type": "tool_search_output", "call_id": "fc_search", "output": "result"},
		map[string]any{"type": "item_reference", "id": "call_search"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "ctc_custom", testassert.MustType[map[string]any](filtered[0])["call_id"])
	require.Equal(t, "ctc_custom", testassert.MustType[map[string]any](filtered[1])["call_id"])
	require.Equal(t, "ctc_custom", testassert.MustType[map[string]any](filtered[2])["id"])
	require.Equal(t, "tsc_search", testassert.MustType[map[string]any](filtered[3])["call_id"])
	require.Equal(t, "tsc_search", testassert.MustType[map[string]any](filtered[4])["call_id"])
	require.Equal(t, "tsc_search", testassert.MustType[map[string]any](filtered[5])["id"])
}

func TestFilterCodexInput_PreservesAmbiguousItemReference(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "call_id": "call_shared", "name": "apply_patch"},
		map[string]any{"type": "tool_search_call", "call_id": "call_shared"},
		map[string]any{"type": "item_reference", "id": "call_shared"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "ctc_shared", testassert.MustType[map[string]any](filtered[0])["call_id"])
	require.Equal(t, "tsc_shared", testassert.MustType[map[string]any](filtered[1])["call_id"])
	require.Equal(t, "call_shared", testassert.MustType[map[string]any](filtered[2])["id"])
}

func TestFilterCodexInput_PreservesNativeItemIDReferenceIndependentlyFromCallID(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "id": "ctc_item", "call_id": "call_custom", "name": "apply_patch"},
		map[string]any{"type": "item_reference", "id": "ctc_item"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "ctc_item", testassert.MustType[map[string]any](filtered[0])["id"])
	require.Equal(t, "ctc_custom", testassert.MustType[map[string]any](filtered[0])["call_id"])
	require.Equal(t, "ctc_item", testassert.MustType[map[string]any](filtered[1])["id"])
}

func TestFilterCodexInput_ExistingItemIDWinsOverLegacyCallIDMapping(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "call_id": "call_shared", "name": "apply_patch"},
		map[string]any{"type": "function_call_output", "id": "call_shared", "call_id": "call_other", "output": "done"},
		map[string]any{"type": "item_reference", "id": "call_shared"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "ctc_shared", testassert.MustType[map[string]any](filtered[0])["call_id"])
	require.Equal(t, "call_shared", testassert.MustType[map[string]any](filtered[1])["id"])
	require.Equal(t, "call_shared", testassert.MustType[map[string]any](filtered[2])["id"])
}

func TestFilterCodexInput_NormalizesCrossTurnLegacyCallReference(t *testing.T) {
	input := []any{
		map[string]any{"type": "item_reference", "id": "call_previous_turn"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "fc_previous_turn", testassert.MustType[map[string]any](filtered[0])["id"])
}

func TestFilterCodexInput_PreservesNativeRemoteItemReferences(t *testing.T) {
	for _, id := range []string{"fc_remote", "ctc_remote", "tsc_remote", "msg_remote", "rs_remote", "vendor_remote"} {
		t.Run(id, func(t *testing.T) {
			input := []any{map[string]any{"type": "item_reference", "id": id}}

			filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

			require.Equal(t, id, testassert.MustType[map[string]any](filtered[0])["id"])
		})
	}
}

// TestFilterCodexInput_StripsItemIDFromAllToolCallInputTypes checks invalid item_* call IDs.
func TestFilterCodexInput_StripsItemIDFromAllToolCallInputTypes(t *testing.T) {
	types := []string{"function_call", "tool_call", "local_shell_call", "tool_search_call", "custom_tool_call", "mcp_tool_call"}

	for _, typ := range types {
		input := []any{
			map[string]any{
				"type":    typ,
				"id":      "item_xyz",
				"call_id": "fc_001",
				"name":    "tool",
			},
		}
		filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
			PreserveReferences: true,
		})
		require.Len(t, filtered, 1)
		item, ok := filtered[0].(map[string]any)
		require.True(t, ok)
		_, hasID := item["id"]
		require.False(t, hasID, "item_* id should be stripped from %s", typ)
	}
}

// TestFilterCodexInput_OutputTypeKeepsItemID verifies function_call_output IDs survive continuation.
func TestFilterCodexInput_OutputTypeKeepsItemID(t *testing.T) {
	input := []any{
		map[string]any{
			"type":    "function_call_output",
			"id":      "o1",
			"call_id": "fc_abc",
			"output":  "done",
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 1)
	out, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "o1", out["id"], "output item id should be preserved")
}

// TestFilterCodexInput_NonToolCallItemKeepsID verifies that a valid web-search ID survives continuation.
func TestFilterCodexInput_NonToolCallItemKeepsID(t *testing.T) {
	input := []any{
		map[string]any{
			"type": "web_search_call",
			"id":   "ws_001",
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 1)
	item, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ws_001", item["id"], "unconstrained items keep their id in preserve mode")
}

// TestFilterCodexInput_StripsMessageItemID_WhenPreservingReferences covers message IDs
// that upstream rejects because they lack the msg prefix.
func TestFilterCodexInput_StripsMessageItemID_WhenPreservingReferences(t *testing.T) {
	input := []any{
		map[string]any{
			"type": "message",
			"id":   "item_3bc5a3fa8ccde25f1c0000d4",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "hello"},
			},
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 1)

	msg, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "message", msg["type"])
	_, hasID := msg["id"]
	require.False(t, hasID, "item_* id should be stripped from message")
	require.Equal(t, "user", msg["role"], "role must be preserved")
	require.NotNil(t, msg["content"], "content must be preserved")
}

// TestFilterCodexInput_KeepsMsgID_WhenPreservingReferences verifies valid message references survive.
func TestFilterCodexInput_KeepsMsgID_WhenPreservingReferences(t *testing.T) {
	input := []any{
		map[string]any{
			"type": "message",
			"id":   "msg_validID123",
			"role": "assistant",
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 1)
	msg, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "msg_validID123", msg["id"], "valid msg* id must be preserved")
}

// TestFilterCodexInput_StripsMessageIDWhenNotPreservingReferences checks both valid and invalid prefixes.
func TestFilterCodexInput_StripsMessageIDWhenNotPreservingReferences(t *testing.T) {
	for _, id := range []string{"item_abc", "msg_validID123"} {
		input := []any{
			map[string]any{
				"type": "message",
				"id":   id,
				"role": "user",
			},
		}

		filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
			PreserveReferences: false,
		})

		require.Len(t, filtered, 1)
		msg, ok := filtered[0].(map[string]any)
		require.True(t, ok)
		_, hasID := msg["id"]
		require.False(t, hasID, "id %q should be stripped when not preserving references", id)
	}
}

// TestFilterCodexInput_MessageIDStripDoesNotMutateInput protects caller-owned maps.
func TestFilterCodexInput_MessageIDStripDoesNotMutateInput(t *testing.T) {
	original := map[string]any{
		"type": "message",
		"id":   "item_abc",
		"role": "user",
	}

	filtered := FilterCodexInputWithOptions([]any{original}, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 1)
	require.Equal(t, "item_abc", original["id"], "original input must not be mutated")
}

// TestFilterCodexInput_MessageStripKeepsFunctionCallBehavior checks separate message and call ID rules.
func TestFilterCodexInput_MessageStripKeepsFunctionCallBehavior(t *testing.T) {
	input := []any{
		map[string]any{
			"type": "message",
			"id":   "item_msg_001",
			"role": "user",
		},
		map[string]any{
			"type":    "function_call",
			"id":      "fc_validID123",
			"call_id": "fc_validID123",
			"name":    "bash",
		},
		map[string]any{
			"type":    "function_call",
			"id":      "item_A9v0SNfS3VaLrfX0j3y4xhyK",
			"call_id": "fc_abc123",
			"name":    "bash",
		},
		map[string]any{
			"type":    "function_call_output",
			"id":      "o1",
			"call_id": "fc_abc123",
			"output":  "done",
		},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{
		PreserveReferences: true,
	})

	require.Len(t, filtered, 4)

	msg, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	_, hasID := msg["id"]
	require.False(t, hasID, "message item_* id should be stripped")

	fcValid, ok := filtered[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_validID123", fcValid["id"], "valid fc* id must be preserved")

	fcBad, ok := filtered[2].(map[string]any)
	require.True(t, ok)
	_, hasID = fcBad["id"]
	require.False(t, hasID, "function_call item_* id should still be stripped")
	require.Equal(t, "fc_abc123", fcBad["call_id"], "call_id pairing must survive")

	out, ok := filtered[3].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "o1", out["id"], "output item id should be preserved")
	require.Equal(t, "fc_abc123", out["call_id"], "call_id pairing must survive")
}

// TestEnsureCodexReasoningInclude checks insertion, idempotence and retention of existing entries.
func TestEnsureCodexReasoningInclude(t *testing.T) {
	// A nonempty reasoning object requires encrypted content in include.
	body := map[string]any{"reasoning": map[string]any{"effort": "medium"}}
	require.True(t, EnsureCodexReasoningInclude(body))
	require.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	// A second call must not duplicate the entry.
	require.False(t, EnsureCodexReasoningInclude(body))

	// Requests without reasoning remain unchanged.
	body2 := map[string]any{}
	require.False(t, EnsureCodexReasoningInclude(body2))
	_, ok := body2["include"]
	require.False(t, ok)

	// Retain existing include entries when appending.
	body3 := map[string]any{
		"reasoning": map[string]any{"effort": "high"},
		"include":   []any{"foo"},
	}
	require.True(t, EnsureCodexReasoningInclude(body3))
	require.Equal(t, []any{"foo", "reasoning.encrypted_content"}, body3["include"])
}

// TestDefaultCodexSynthInstructionsModelAware checks model-specific embedded prompts.
func TestDefaultCodexSynthInstructionsModelAware(t *testing.T) {
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5-codex"), "You are Codex, based on GPT-5"))
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.5"), "You are Codex, a coding agent based on GPT-5"))
	require.False(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.5"), "You are GPT-5.1 running in the Codex CLI"))
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.2"), "You are GPT-5.2 running in the Codex CLI"))
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.1"), "You are GPT-5.1 running in the Codex CLI"))
}
