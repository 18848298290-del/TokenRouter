package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	testassert "github.com/TokenFlux/TokenRouter/internal/testutil/assertion"
)

// TestFilterCodexInput_StripsFunctionCallItemID_WhenPreservingReferences
// supplies an incompatible item ID on a paired function call. Filtering removes
// that ID while retaining type, name, and call_id used for its result.
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

// TestFilterCodexInput_KeepsFcID_WhenPreservingReferences supplies a function
// call with an accepted fc item ID. Continuation filtering keeps that remote
// item identity in the returned single item.
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

// TestFilterCodexInput_PreservesNativeCustomAndToolSearchIDs supplies native
// custom and search item identities with legacy call IDs. Filtering retains
// each ctc/tsc item ID independently of call-ID normalization.
func TestFilterCodexInput_PreservesNativeCustomAndToolSearchIDs(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "id": "ctc_valid", "call_id": "call_custom", "name": "apply_patch"},
		map[string]any{"type": "tool_search_call", "id": "tsc_valid", "call_id": "call_search"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "ctc_valid", testassert.MustType[map[string]any](filtered[0])["id"])
	require.Equal(t, "tsc_valid", testassert.MustType[map[string]any](filtered[1])["id"])
}

// TestFilterCodexInput_StripsWrongCustomAndToolSearchIDs supplies fc item IDs
// on custom and search calls. Filtering removes both incompatible item
// identities while retaining their returned item maps.
func TestFilterCodexInput_StripsWrongCustomAndToolSearchIDs(t *testing.T) {
	input := []any{
		map[string]any{"type": "custom_tool_call", "id": "fc_wrong", "call_id": "call_custom", "name": "apply_patch"},
		map[string]any{"type": "tool_search_call", "id": "fc_wrong", "call_id": "call_search"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.NotContains(t, testassert.MustType[map[string]any](filtered[0]), "id")
	require.NotContains(t, testassert.MustType[map[string]any](filtered[1]), "id")
}

// TestFilterCodexInput_MapsItemReferencesToNativeToolCallPair supplies
// custom/search calls, outputs, and legacy call references. Returned call IDs,
// paired output IDs, and rewritten references agree in their ctc/tsc
// namespaces.
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

// TestFilterCodexInput_PreservesAmbiguousItemReference assigns one legacy call
// ID to custom and search calls. Both calls normalize independently, while the
// ambiguous reference keeps its supplied spelling.
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

// TestFilterCodexInput_PreservesNativeItemIDReferenceIndependentlyFromCallID
// gives a custom call separate item and legacy call identities. Filtering
// normalizes call_id while retaining the native item ID and its reference.
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

// TestFilterCodexInput_ExistingItemIDWinsOverLegacyCallIDMapping creates a call
// mapping that conflicts with an actual output item ID. The reference retains
// that existing item identity while the custom call normalizes.
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

// TestFilterCodexInput_NormalizesCrossTurnLegacyCallReference supplies a legacy
// reference with no same-turn call. Filtering rewrites its ID to the function
// namespace for an earlier-turn call.
func TestFilterCodexInput_NormalizesCrossTurnLegacyCallReference(t *testing.T) {
	input := []any{
		map[string]any{"type": "item_reference", "id": "call_previous_turn"},
	}

	filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

	require.Equal(t, "fc_previous_turn", testassert.MustType[map[string]any](filtered[0])["id"])
}

// TestFilterCodexInput_PreservesNativeRemoteItemReferences supplies native and
// vendor-prefixed remote references without local items. Preserve mode retains
// each ID verbatim, covering references resolved outside this input sequence.
func TestFilterCodexInput_PreservesNativeRemoteItemReferences(t *testing.T) {
	for _, id := range []string{"fc_remote", "ctc_remote", "tsc_remote", "msg_remote", "rs_remote", "vendor_remote"} {
		t.Run(id, func(t *testing.T) {
			input := []any{map[string]any{"type": "item_reference", "id": id}}

			filtered := FilterCodexInputWithOptions(input, CodexInputFilterOptions{PreserveReferences: true})

			require.Equal(t, id, testassert.MustType[map[string]any](filtered[0])["id"])
		})
	}
}

// TestFilterCodexInput_StripsItemIDFromAllToolCallInputTypes supplies item_ IDs
// across the supported call-input types. Every returned item loses that
// incompatible ID while the one-item result shape remains.
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

// TestFilterCodexInput_OutputTypeKeepsItemID supplies an unconstrained
// function-output item ID during continuation. Filtering retains that ID in the
// returned output map independently of its pairing call_id.
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

// TestFilterCodexInput_NonToolCallItemKeepsID supplies an accepted ws
// web-search item identity in preserve mode. Filtering returns one map
// retaining that ID under its search-specific prefix rule.
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

// TestFilterCodexInput_StripsMessageItemID_WhenPreservingReferences supplies an
// incompatible item_ identity on a user message. Filtering removes that ID
// while retaining the user role, message type, and content.
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

// TestFilterCodexInput_KeepsMsgID_WhenPreservingReferences supplies a message
// with an accepted msg identity during continuation. The returned single
// message retains that remote item ID for subsequent references.
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

// TestFilterCodexInput_StripsMessageIDWhenNotPreservingReferences disables
// reference retention for valid and invalid message IDs. Both returned messages
// lose id, protecting the mode decision separately from prefix validity.
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

// TestFilterCodexInput_MessageIDStripDoesNotMutateInput passes a caller-owned
// message map needing ID cleanup. It asserts that the supplied map keeps its ID
// and that the filtered sequence still contains one item.
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

// TestFilterCodexInput_MessageStripKeepsFunctionCallBehavior combines a
// message, valid/invalid calls, and a paired output. It checks distinct item-ID
// rules and keeps the call/result pairing IDs in the returned four items.
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

// TestEnsureCodexReasoningInclude supplies reasoning with absent and existing
// include lists, then repeats insertion. The assertions protect idempotence,
// retention order, and an unchanged request lacking reasoning.
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

// TestDefaultCodexSynthInstructionsModelAware selects embedded guidance for
// several GPT/Codex model names. It checks identifying prompt text and excludes
// an incorrect older identity for the tested gpt-5.5 case.
func TestDefaultCodexSynthInstructionsModelAware(t *testing.T) {
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5-codex"), "You are Codex, based on GPT-5"))
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.5"), "You are Codex, a coding agent based on GPT-5"))
	require.False(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.5"), "You are GPT-5.1 running in the Codex CLI"))
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.2"), "You are GPT-5.2 running in the Codex CLI"))
	require.True(t, strings.Contains(DefaultCodexSynthInstructions("gpt-5.1"), "You are GPT-5.1 running in the Codex CLI"))
}
